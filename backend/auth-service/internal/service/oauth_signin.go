package service

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
)

// Errors for Google account linking.
var (
	// ErrGoogleSubjectTaken reports that another callback linked this Google
	// identity first; resolving again finds that link.
	ErrGoogleSubjectTaken = errors.New("google identity is already linked")
	// ErrGoogleAccountLinked reports that the matching account is linked to a
	// different Google identity.
	ErrGoogleAccountLinked = errors.New("account is linked to another google identity")
	// ErrAccountUnavailable reports an account that may not sign in, such as
	// one being deleted.
	ErrAccountUnavailable = errors.New("account cannot sign in")
)

const (
	maxResolveAttempts  = 3
	maxUsernameAttempts = 10
	usernameMaxLength   = 20
)

// GoogleAccountStore reads and writes Google links.
type GoogleAccountStore interface {
	// FindGoogleUser returns the account linked to subject, or ErrNotFound.
	FindGoogleUser(ctx context.Context, subject string) (model.User, error)
	// LinkGoogle links subject to userID. It is idempotent for the same pair
	// and returns ErrGoogleSubjectTaken or ErrGoogleAccountLinked otherwise.
	LinkGoogle(ctx context.Context, subject string, userID int64) error
}

// GoogleLinkTx creates a link in the transaction that creates its account.
type GoogleLinkTx interface {
	CreateGoogleLink(ctx context.Context, subject string, userID int64) error
}

// SignInWithGoogle resolves a verified Google identity to an account and session.
//
// Resolution follows the contract:
//
//  1. An account already linked to this Google identity signs in.
//  2. Otherwise an account whose email matches (case-insensitive) is linked.
//  3. Otherwise a password-less account is created with a generated unique
//     username and provisioned like a registration.
//
// A concurrent callback that wins a race is found on the next pass. Only an
// ACTIVE account receives a session; a PROVISIONING one resumes its stored
// operation first, and a DELETING one is refused.
//
// Errors:
//
//   - ErrGoogleAccountLinked: the email's account belongs to another Google identity.
//   - ErrAccountUnavailable: the account is being deleted.
//   - ErrServiceUnavailable, ErrProvisioningFailed: provisioning could not finish.
func (s *AuthService) SignInWithGoogle(ctx context.Context, identity GoogleIdentity) (AuthResult, error) {
	store, ok := s.repository.(GoogleAccountStore)
	if !ok || s.sessions == nil || s.tokens == nil {
		return AuthResult{}, ErrSessionUnavailable
	}
	for attempt := 0; attempt < maxResolveAttempts; attempt++ {
		user, err := store.FindGoogleUser(ctx, identity.Subject)
		if err == nil {
			return s.finishGoogleSignIn(ctx, user)
		}
		if !errors.Is(err, ErrNotFound) {
			return AuthResult{}, err
		}

		user, err = s.sessions.FindUserByEmail(ctx, identity.Email)
		switch {
		case err == nil:
			err = store.LinkGoogle(ctx, identity.Subject, user.ID)
			if errors.Is(err, ErrGoogleSubjectTaken) {
				continue
			}
			if err != nil {
				return AuthResult{}, err
			}
			return s.finishGoogleSignIn(ctx, user)
		case !errors.Is(err, ErrNotFound):
			return AuthResult{}, err
		}

		user, err = s.createGoogleAccount(ctx, identity)
		if errors.Is(err, ErrEmailTaken) || errors.Is(err, ErrGoogleSubjectTaken) {
			continue
		}
		if err != nil {
			return AuthResult{}, err
		}
		return s.createLoginSession(ctx, user)
	}

	return AuthResult{}, ErrOAuthProviderError
}

func (s *AuthService) finishGoogleSignIn(ctx context.Context, user model.User) (AuthResult, error) {
	switch user.Status {
	case model.AccountActive:
	case model.AccountProvisioning:
		activated, err := s.resumeProvisioning(ctx, user)
		if err != nil {
			return AuthResult{}, err
		}
		user = activated
	default:
		return AuthResult{}, ErrAccountUnavailable
	}

	return s.createLoginSession(ctx, user)
}

// createGoogleAccount creates a password-less account linked to identity and
// returns it once it is ACTIVE.
//
// It tries generated usernames in order. A username reported
// taken by User Service (which discards the provisional account) moves on to
// the next candidate.
func (s *AuthService) createGoogleAccount(ctx context.Context, identity GoogleIdentity) (model.User, error) {
	store, provisioning := s.repository.(ProvisioningRepository)
	provisioning = provisioning && s.provisioningEnabled()
	for attempt := 1; attempt <= maxUsernameAttempts; attempt++ {
		username := GoogleUsername(identity.Name, identity.Email, attempt)
		status := model.AccountActive
		if provisioning {
			status = model.AccountProvisioning
		}

		var created model.User
		var operation model.ProvisioningOperation
		err := s.repository.WithTransaction(ctx, func(tx AuthTx) error {
			linkTx, ok := tx.(GoogleLinkTx)
			if !ok {
				return ErrSessionUnavailable
			}
			user, err := tx.CreateUser(ctx, model.User{Email: identity.Email, Username: username, Status: status})
			if err != nil {
				return err
			}
			if err := linkTx.CreateGoogleLink(ctx, identity.Subject, user.ID); err != nil {
				return err
			}
			created = user
			if !provisioning {
				return nil
			}
			provisioningTx, ok := tx.(ProvisioningTx)
			if !ok {
				return ErrSessionUnavailable
			}
			operation, err = newProvisioningOperation(user)
			if err != nil {
				return err
			}
			return provisioningTx.CreateProvisioningOperation(ctx, operation)
		})
		if err != nil {
			return model.User{}, err
		}
		if !provisioning {
			return created, nil
		}

		activated, err := s.completeProvisioning(ctx, store, operation)
		if errors.Is(err, ErrUsernameTaken) {
			continue
		}
		return activated, err
	}

	return model.User{}, ErrUsernameTaken
}

// usernameBase derives a username stem from the Google name, or else from the
// email local part. Letters and digits are kept; spaces, dots and dashes
// become underscores; other characters are dropped.
func usernameBase(name, email string) string {
	for _, source := range []string{name, strings.SplitN(email, "@", 2)[0]} {
		var b strings.Builder
		for _, r := range source {
			switch {
			case r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'):
				b.WriteRune(r)
			case r == ' ' || r == '.' || r == '-' || r == '_':
				if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
					b.WriteByte('_')
				}
			}
		}
		stem := strings.Trim(b.String(), "_")
		if len(stem) > usernameMaxLength {
			stem = strings.TrimRight(stem[:usernameMaxLength], "_")
		}
		if len(stem) >= 3 {
			return stem
		}
	}

	return "player"
}

// usernameCandidate returns base for the first attempt and base_N afterwards,
// shortened so the result stays within 20 characters.
func usernameCandidate(base string, attempt int) string {
	if attempt == 1 {
		return base
	}
	suffix := "_" + strconv.Itoa(attempt)
	if len(base)+len(suffix) > usernameMaxLength {
		base = strings.TrimRight(base[:usernameMaxLength-len(suffix)], "_")
	}

	return base + suffix
}

// GoogleUsername returns the username tried on the given attempt (1-based)
// for a new Google account.
func GoogleUsername(name, email string, attempt int) string {
	return usernameCandidate(usernameBase(name, email), attempt)
}
