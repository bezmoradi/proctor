package api

import "github.com/bezmoradi/proctor/examples/sample/internal/core/user/domain"

type User struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

func NewUser(u domain.User) User {
	return User{ID: u.ID, Name: u.Name}
}

func NewUsers(users []domain.User) []User {
	if users == nil {
		return nil
	}

	out := make([]User, 0, len(users))
	for _, u := range users {
		out = append(out, NewUser(u))
	}

	return out
}
