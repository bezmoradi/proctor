package domain

import (
	"errors"
	"unicode"
	"unicode/utf8"
)

const MaxNameLength = 100

const (
	DefaultPageLimit = 20
	MaxPageLimit     = 100
)

var (
	ErrUserNotFound       = errors.New("user not found")
	ErrNameRequired       = errors.New("name is required")
	ErrNameTooLong        = errors.New("name is too long")
	ErrNameInvalid        = errors.New("name contains invalid characters")
	ErrEventPublishFailed = errors.New("event publish failed")
	ErrInvalidCursor      = errors.New("invalid pagination cursor")
)

func IsValidationError(err error) bool {
	return errors.Is(err, ErrNameRequired) ||
		errors.Is(err, ErrNameTooLong) ||
		errors.Is(err, ErrNameInvalid)
}

func ValidateName(name string) error {
	if name == "" {
		return ErrNameRequired
	}

	if utf8.RuneCountInString(name) > MaxNameLength {
		return ErrNameTooLong
	}

	if !utf8.ValidString(name) {
		return ErrNameInvalid
	}

	for _, r := range name {
		if unicode.IsControl(r) {
			return ErrNameInvalid
		}
	}

	return nil
}

type User struct {
	ID   string
	Name string
}

type Page struct {
	Items      []User
	NextCursor string
}
