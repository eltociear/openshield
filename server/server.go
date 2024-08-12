// @title OpenShield API
// @version 1.0
// @description This is the API server for OpenShield.

package server

import (
	"context"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"
	httprateredis "github.com/go-chi/httprate-redis"
	_ "github.com/openshieldai/openshield/docs"
	"github.com/openshieldai/openshield/lib"
	"github.com/openshieldai/openshield/lib/openai"
	"golang.org/x/sync/errgroup"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

var (
	router chi.Router
	config lib.Configuration
)

// ErrorResponse represents the structure of error responses
type ErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Param   string `json:"param"`
		Code    string `json:"code"`
	} `json:"error"`
}

// ListModelsHandler @Summary List models
// @Description Get a list of available models
// @Tags openai
// @Produce json
// @Success 200 {object} openai.ModelsList
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /openai/v1/models [get]
func ListModelsHandler(w http.ResponseWriter, r *http.Request) {
	openai.ListModelsHandler(w, r)
}

// GetModelHandler @Summary Get model details
// @Description Get details of a specific model
// @Tags openai
// @Produce json
// @Param model path string true "Model ID"
// @Success 200 {object} openai.Model
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /openai/v1/models/{model} [get]
func GetModelHandler(w http.ResponseWriter, r *http.Request) {
	openai.GetModelHandler(w, r)
}

// ChatCompletionHandler @Summary Create chat completion
// @Description Create a chat completion
// @Tags openai
// @Accept json
// @Produce json
// @Param request body openai.ChatCompletionRequest true "Chat completion request"
// @Success 200 {object} openai.ChatCompletionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /openai/v1/chat/completions [post]
func ChatCompletionHandler(w http.ResponseWriter, r *http.Request) {
	openai.ChatCompletionHandler(w, r)
}

func StartServer() error {
	config = lib.GetConfig()

	router = chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Logger)
	router.Use(middleware.Recoverer)
	router.Use(middleware.Timeout(60 * time.Second))

	// CORS configuration
	router.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			next.ServeHTTP(w, r)
		})
	})

	setupOpenAIRoutes(router)
	//TODO
	// Swagger route, relevant: https://github.com/swaggo/http-swagger
	//	router.Get("/swagger/*", swagger.HandlerDefault)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	g, ctx := errgroup.WithContext(ctx)

	// Start the server
	g.Go(func() error {
		addr := fmt.Sprintf(":%d", config.Settings.Network.Port)
		fmt.Printf("Server is starting on %s...\n", addr)
		return http.ListenAndServe(addr, router)
	})

	// Handle graceful shutdown
	g.Go(func() error {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

		select {
		case <-quit:
			fmt.Println("Shutting down server...")
			cancel()
		case <-ctx.Done():
			return ctx.Err()
		}

		return nil
	})

	if err := g.Wait(); err != nil {
		fmt.Printf("Server error: %v\n", err)
		return err
	}

	return nil
}

func StopServer() error {
	fmt.Println("Stopping the server...")
	//TODO
	//Chi doesn't have a built-in server shutdown method
	//relevant : https://github.com/go-chi/chi/issues/58
	return nil
}

func setupOpenAIRoutes(r chi.Router) {
	r.Route("/openai/v1", func(r chi.Router) {
		r.Get("/models", lib.AuthOpenShieldMiddleware(openai.ListModelsHandler))
		r.Get("/models/{model}", lib.AuthOpenShieldMiddleware(openai.GetModelHandler))
		r.Post("/chat/completions", lib.AuthOpenShieldMiddleware(openai.ChatCompletionHandler))
	})
}

func setupRoute(r chi.Router, path string, routeSettings lib.RouteSettings, handler http.HandlerFunc) {
	Max := routeSettings.RateLimit.Max
	Expiration := time.Duration(routeSettings.RateLimit.Expiration) * time.Second * time.Duration(routeSettings.RateLimit.Window)

	// Parse the Redis URL
	redisURL, err := url.Parse(routeSettings.Redis.URI)
	if err != nil {
		panic(err)
	}

	host := redisURL.Hostname()
	port, _ := strconv.Atoi(redisURL.Port())

	redisConfig := &httprateredis.Config{
		Host:     host,
		Port:     uint16(port),
		Password: redisURL.User.Username(),
	}

	redisCounter, err := httprateredis.NewRedisLimitCounter(redisConfig)
	if err != nil {
		panic(err)
	}

	rateLimiter := httprate.Limit(
		Max,
		Expiration,
		httprate.WithLimitCounter(redisCounter),
		httprate.WithKeyFuncs(httprate.KeyByIP),
	)

	r.With(rateLimiter).Handle(path, handler)
}
