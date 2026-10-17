package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

const tokenReviewTTL = time.Minute

type tokenReviewEntry struct {
	user    pkgauth.User
	expires time.Time
}

var (
	tokenReviewMu    sync.Mutex
	tokenReviewCache = map[[sha256.Size]byte]tokenReviewEntry{}
)

// ReviewUserToken asks the API server who a user token belongs to
// (SelfSubjectReview, made with that token and nothing else). The answer is
// cached briefly by token hash, never past the token's own expiry.
func ReviewUserToken(ctx context.Context, token string) (*pkgauth.User, error) {
	key := sha256.Sum256([]byte(token))
	now := time.Now()
	tokenReviewMu.Lock()
	if e, ok := tokenReviewCache[key]; ok && now.Before(e.expires) {
		tokenReviewMu.Unlock()
		u := e.user
		return &u, nil
	}
	tokenReviewMu.Unlock()

	base := GetConfig()
	if base == nil {
		return nil, fmt.Errorf("K8s config not initialized")
	}
	cfg := rest.AnonymousClientConfig(base)
	cfg.BearerToken = token
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	review, err := client.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authnv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	info := review.Status.UserInfo
	if info.Username == "" {
		return nil, fmt.Errorf("the API server returned no username for this token")
	}
	user := pkgauth.User{Username: info.Username, Groups: info.Groups, UID: info.UID}
	if len(info.Extra) > 0 {
		user.Extra = make(map[string][]string, len(info.Extra))
		for k, v := range info.Extra {
			user.Extra[k] = append([]string(nil), v...)
		}
	}

	expires := now.Add(tokenReviewTTL)
	if exp, ok := jwtExpiry(token); ok && exp.Before(expires) {
		expires = exp
	}
	tokenReviewMu.Lock()
	if len(tokenReviewCache) > 4096 {
		tokenReviewCache = map[[sha256.Size]byte]tokenReviewEntry{}
	}
	tokenReviewCache[key] = tokenReviewEntry{user: user, expires: expires}
	tokenReviewMu.Unlock()
	return &user, nil
}

// jwtExpiry reads exp from a JWT without verifying it. Only used to bound a
// cache entry for a token the API server has already accepted.
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}
