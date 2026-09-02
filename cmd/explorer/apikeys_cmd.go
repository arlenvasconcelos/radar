package main

// `radar api-keys <sub>` — administrative API key management, run where the
// key database lives: inside the pod (`kubectl exec`) for an in-cluster
// install, or on the host for a VM install.
//
//	radar api-keys list    [--user NAME]
//	radar api-keys revoke   --id ID | --user NAME | --all
//
// Deliberately file-scoped rather than an HTTP admin route. Radar has no admin
// identity of its own — authorization is Kubernetes RBAC — so the honest gate
// is reaching the database at all, which already requires exec into the pod or
// root on the host. Anyone with that can read the in-memory cache of every
// Secret and the session signing key; an identity check layered on top would
// be theater.
//
// It is also the only way to revoke a departed user's keys. The HTTP routes
// are owner-scoped by design, so once an IdP account is disabled nobody can
// revoke through them.

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"golang.org/x/term"
)

// defaultAPIKeyStorePath is where the server puts the key database when
// --auth-api-keys-file is unset. Shared with openAPIKeyStore so the CLI and
// the server can never disagree about which file is the real one.
func defaultAPIKeyStorePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".radar", "api-keys.db"), nil
}

// runAPIKeysSubcommand handles `radar api-keys …` before the flat flag set is
// parsed.
func runAPIKeysSubcommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		apiKeysUsage(stderr)
		return 2
	}
	switch args[0] {
	case "list":
		return apiKeysList(args[1:], stdout, stderr)
	case "revoke":
		return apiKeysRevoke(args[1:], stdout, stderr, os.Stdin)
	case "-h", "--help", "help":
		apiKeysUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "radar api-keys: unknown subcommand %q\n\n", args[0])
		apiKeysUsage(stderr)
		return 2
	}
}

func apiKeysUsage(w io.Writer) {
	fmt.Fprint(w, `Administer the per-user API keys of a Radar installation.

Usage:
  radar api-keys list   [--file PATH] [--user NAME]
  radar api-keys revoke [--file PATH] (--id ID | --user NAME | --all) [-y|--yes]

list     Show every key in the database: id, owner, description, creation time.
         Key material is not stored and cannot be shown. --user filters to one
         owner.

revoke   Delete keys permanently. Revocation takes effect on the next request
         the key is used for. Exactly one selector is required:
           --id ID      revoke one key, whoever owns it
           --user NAME  revoke every key owned by NAME (offboarding)
           --all        revoke every key in the database

Flags:
  --file PATH  API key database (default: ~/.radar/api-keys.db, the same path
               --auth-api-keys-file resolves to). Must already exist.
  -y, --yes    Skip the confirmation prompt. Required when stdin is not a
               terminal, which includes kubectl exec without -it.

Run this where the database lives — inside the Radar pod, or on the host of a
VM install. API keys exist only when Radar runs with --auth-mode=proxy or
--auth-mode=oidc; a local no-auth instance has no key database.
`)
}

// openAdminKeyStore resolves the database path and opens it without creating
// it. A missing file is reported as configuration guidance rather than an
// empty store, since "no keys" and "wrong path" would otherwise look alike.
func openAdminKeyStore(path string, stderr io.Writer) (*auth.SQLiteAPIKeyStore, int) {
	if path == "" {
		resolved, err := defaultAPIKeyStorePath()
		if err != nil {
			fmt.Fprintf(stderr, "radar api-keys: cannot determine the default database path: %v\n", err)
			fmt.Fprintln(stderr, "Pass --file PATH explicitly.")
			return nil, 1
		}
		path = resolved
	}
	store, err := auth.OpenExistingAPIKeyStore(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(stderr, "radar api-keys: no API key database at %s\n", path)
			fmt.Fprintln(stderr, "Radar creates one only under --auth-mode=proxy or --auth-mode=oidc.")
			fmt.Fprintln(stderr, "If the server uses --auth-api-keys-file, pass the same path with --file.")
			return nil, 1
		}
		fmt.Fprintf(stderr, "radar api-keys: %v\n", err)
		return nil, 1
	}
	return store, 0
}

func apiKeysList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("api-keys list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("file", "", "API key database (default: ~/.radar/api-keys.db)")
	user := fs.String("user", "", "only show keys owned by this user")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	store, code := openAdminKeyStore(*path, stderr)
	if store == nil {
		return code
	}
	defer store.Close()

	var (
		keys []pkgauth.APIKey
		err  error
	)
	if *user != "" {
		keys, err = store.ListForUser(*user)
	} else {
		keys, err = store.ListAll()
	}
	if err != nil {
		fmt.Fprintf(stderr, "radar api-keys: %v\n", err)
		return 1
	}
	if len(keys) == 0 {
		if *user != "" {
			fmt.Fprintf(stdout, "No API keys for %q.\n", *user)
		} else {
			fmt.Fprintln(stdout, "No API keys.")
		}
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tUSER\tCREATED\tDESCRIPTION")
	for _, key := range keys {
		created := "-"
		if !key.CreatedAt.IsZero() {
			created = key.CreatedAt.UTC().Format(time.RFC3339)
		}
		description := key.Description
		if description == "" {
			description = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", key.ID, key.Username, created, description)
	}
	tw.Flush()
	return 0
}

func apiKeysRevoke(args []string, stdout, stderr io.Writer, stdin *os.File) int {
	fs := flag.NewFlagSet("api-keys revoke", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("file", "", "API key database (default: ~/.radar/api-keys.db)")
	id := fs.String("id", "", "revoke one key by id")
	user := fs.String("user", "", "revoke every key owned by this user")
	all := fs.Bool("all", false, "revoke every key in the database")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	fs.BoolVar(yes, "y", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Exactly one selector: --all next to a narrower flag is ambiguous enough
	// that guessing could revoke the whole database instead of one key.
	selectors := []string{}
	if *id != "" {
		selectors = append(selectors, "--id")
	}
	if *user != "" {
		selectors = append(selectors, "--user")
	}
	if *all {
		selectors = append(selectors, "--all")
	}
	switch len(selectors) {
	case 1:
	case 0:
		fmt.Fprintln(stderr, "radar api-keys revoke: one of --id, --user or --all is required")
		return 2
	default:
		fmt.Fprintf(stderr, "radar api-keys revoke: %s are mutually exclusive\n", strings.Join(selectors, " and "))
		return 2
	}

	store, code := openAdminKeyStore(*path, stderr)
	if store == nil {
		return code
	}
	defer store.Close()

	// Describe the exact blast radius before asking, so --all can't be
	// confirmed on the assumption it means "all of that user's keys".
	var subject string
	switch {
	case *id != "":
		subject = fmt.Sprintf("API key %s", *id)
	case *user != "":
		keys, err := store.ListForUser(*user)
		if err != nil {
			fmt.Fprintf(stderr, "radar api-keys: %v\n", err)
			return 1
		}
		if len(keys) == 0 {
			fmt.Fprintf(stdout, "No API keys for %q.\n", *user)
			return 0
		}
		subject = fmt.Sprintf("%s owned by %q", pluralKeys(len(keys)), *user)
	default:
		keys, err := store.ListAll()
		if err != nil {
			fmt.Fprintf(stderr, "radar api-keys: %v\n", err)
			return 1
		}
		if len(keys) == 0 {
			fmt.Fprintln(stdout, "No API keys.")
			return 0
		}
		subject = fmt.Sprintf("%s across %s", pluralKeys(len(keys)), pluralUsers(countOwners(keys)))
	}

	if !*yes {
		ok, err := confirmRevoke(subject, stdout, stderr, stdin)
		if err != nil {
			fmt.Fprintf(stderr, "radar api-keys: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "Aborted. Nothing was revoked.")
			return 1
		}
	}

	switch {
	case *id != "":
		deleted, err := store.DeleteByID(*id)
		if err != nil {
			fmt.Fprintf(stderr, "radar api-keys: %v\n", err)
			return 1
		}
		if !deleted {
			fmt.Fprintf(stderr, "radar api-keys: no key with id %q\n", *id)
			return 1
		}
		fmt.Fprintf(stdout, "Revoked API key %s.\n", *id)
	case *user != "":
		n, err := store.DeleteAllForUser(*user)
		if err != nil {
			fmt.Fprintf(stderr, "radar api-keys: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Revoked %s owned by %q.\n", pluralKeys(int(n)), *user)
	default:
		n, err := store.DeleteAll()
		if err != nil {
			fmt.Fprintf(stderr, "radar api-keys: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Revoked %s.\n", pluralKeys(int(n)))
	}
	return 0
}

// confirmRevoke requires a typed "yes". Revocation is irreversible — the
// plaintext cannot be recovered, so a wrongly revoked key must be re-minted by
// its owner, which a departed user cannot do.
func confirmRevoke(subject string, stdout, stderr io.Writer, stdin *os.File) (bool, error) {
	if !isTerminal(stdin) {
		return false, errors.New("stdin is not a terminal; re-run with --yes (or `kubectl exec -it`) to confirm")
	}
	fmt.Fprintf(stdout, "About to permanently revoke %s.\n", subject)
	fmt.Fprint(stdout, "Clients using them will start failing on their next request. Type 'yes' to continue: ")
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(line), "yes"), nil
}

// isTerminal must not settle for "is a character device": /dev/null is one,
// and treating it as a terminal would turn an unattended `--all` into a
// confirmation satisfied by an immediate EOF.
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

func countOwners(keys []pkgauth.APIKey) int {
	owners := map[string]struct{}{}
	for _, key := range keys {
		owners[key.Username] = struct{}{}
	}
	return len(owners)
}

func pluralKeys(n int) string {
	if n == 1 {
		return "1 API key"
	}
	return fmt.Sprintf("%d API keys", n)
}

func pluralUsers(n int) string {
	if n == 1 {
		return "1 user"
	}
	return fmt.Sprintf("%d users", n)
}
