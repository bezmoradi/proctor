package http

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bezmoradi/proctor/examples/sample/internal/adapters/api"
	"github.com/bezmoradi/proctor/examples/sample/internal/core/shared"
	"github.com/bezmoradi/proctor/examples/sample/internal/core/user/domain"
	"github.com/bezmoradi/proctor/examples/sample/internal/core/user/ports"
	"net/http"
)

type router struct {
	engine       *http.ServeMux
	globalPrefix string
	service      ports.UserServiceInterface
	logger       shared.Logger
}

func NewRouter(service ports.UserServiceInterface, logger shared.Logger) http.Handler {
	r := &router{
		engine:       http.NewServeMux(),
		globalPrefix: "/api/v1",
		service:      service,
		logger:       logger,
	}
	r.registerUserRoutes()
	return r.engine
}

func (router router) fail(w http.ResponseWriter, operation string, err error) {
	router.logger.Error(operation+" failed", "error", err)
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(api.Response{Error: failureLabel(err)})
}

func failureLabel(err error) api.ErrorMessageType {
	if errors.Is(err, domain.ErrEventPublishFailed) {
		return api.EventPublishFailure
	}
	return api.DatabaseOperationFailure
}

func (router router) registerUserRoutes() {
	prefix := router.globalPrefix + "/users"

	router.engine.HandleFunc(fmt.Sprintf("GET %v", prefix), router.IndexHandler)
	router.engine.HandleFunc(fmt.Sprintf("GET %v/{id}", prefix), router.ShowUserHandler)
	router.engine.HandleFunc(fmt.Sprintf("POST %v", prefix), router.CreateUserHandler)
	router.engine.HandleFunc(fmt.Sprintf("PUT %v/{id}", prefix), router.UpdateUserHandler)
	router.engine.HandleFunc(fmt.Sprintf("DELETE %v/{id}", prefix), router.DeleteUserHandler)
}

func pathID(r *http.Request) string {
	return r.PathValue("id")
}
