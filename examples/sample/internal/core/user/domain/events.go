package domain

type EventKind string

const (
	UserCreated EventKind = "user.created"
	UserUpdated EventKind = "user.updated"
	UserDeleted EventKind = "user.deleted"
)

type Event struct {
	Kind EventKind
	User User
}
