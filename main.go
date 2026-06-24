package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/xfac11/chirpy/internal/database"
)

type apiConfig struct {
	fileserverHits atomic.Int32
	dbQueries      *database.Queries
	platform       string
}

type User struct {
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Email     string    `json:"email"`
}

func (cfg *apiConfig) middlewareMetricsInc(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg.fileserverHits.Add(1)
		next.ServeHTTP(w, r)
	})
}

func (cfg *apiConfig) metricsHandler(responseWrite http.ResponseWriter, request *http.Request) {
	responseWrite.Header().Set("Content-Type", "text/html; charset=utf-8")
	responseWrite.WriteHeader(http.StatusOK)

	requestsText := fmt.Sprintf("<html><body><h1>Welcome, Chirpy Admin</h1><p>Chirpy has been visited %d times!</p></body></html>", cfg.fileserverHits.Load())
	var body = []byte(requestsText)
	responseWrite.Write(body)
}

func (cfg *apiConfig) resetHandler(responeWrite http.ResponseWriter, request *http.Request) {
	if cfg.platform != "dev" {
		jsonData, _ := writeJsondataError("Forbidden request")

		responeWrite.Header().Set("Content-Type", "application/json")
		responeWrite.WriteHeader(http.StatusForbidden)
		responeWrite.Write(jsonData)

		return
	}

	cfg.fileserverHits.Store(0)

	err := cfg.dbQueries.DeleteAllUsers(request.Context())
	if err != nil {
		jsonData, _ := writeJsondataError("Something went wrong when deleting all users")

		responeWrite.Header().Set("Content-Type", "application/json")
		responeWrite.WriteHeader(http.StatusInternalServerError)
		responeWrite.Write(jsonData)

		return
	}

	var body = []byte("OK")
	responeWrite.Header().Set("Content-Type", "text/plain; charset=utf-8")
	responeWrite.WriteHeader(http.StatusOK)
	responeWrite.Write(body)
}

func (cfg *apiConfig) createUserHandler(response http.ResponseWriter, request *http.Request) {
	type parameters struct {
		Email string `json:"email"`
	}
	params := parameters{}
	decoder := json.NewDecoder(request.Body)
	err := decoder.Decode(&params)
	if err != nil {
		log.Printf("Could not decode request body into a struct: %s", err)
		jsonData, _ := writeJsondataError("Something went wrong")
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusInternalServerError)
		response.Write(jsonData)
		return
	}

	email := params.Email
	dbUser, err := cfg.dbQueries.CreateUser(request.Context(), email)
	if err != nil {
		log.Printf("Could not create a user using email: %s, error: %s", email, err)
		jsonData, _ := writeJsondataError("A user with that email already exists")
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusConflict)
		response.Write(jsonData)
		return
	}

	user := User{
		ID:        dbUser.ID,
		CreatedAt: dbUser.CreatedAt,
		UpdatedAt: dbUser.UpdatedAt,
		Email:     dbUser.Email,
	}

	jsonUser, err := json.Marshal(user)
	if err != nil {
		log.Printf("Could not marshal to json encoding of main.User: %s", err)
		jsonData, _ := writeJsondataError("Something went wrong")
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusInternalServerError)
		response.Write(jsonData)
		return
	}

	log.Printf("Successfully created a user with id: %s", user.ID)
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusCreated)
	response.Write(jsonUser)
}

func middlewareLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func writeJsondataError(text string) ([]byte, error) {
	type returnVals struct {
		Error string `json:"error"`
	}
	respBody := returnVals{
		Error: text,
	}
	jsonData, err := json.Marshal(respBody)
	return jsonData, err
}

func removeProfanity(text string, badWords []string, replace string) string {
	splitBody := strings.Split(text, " ")
	for i, word := range splitBody {
		word = strings.ToLower(word)
		for _, badWord := range badWords {
			if strings.Contains(word, badWord) && len(word) == len(badWord) {
				word = strings.ReplaceAll(word, badWord, replace)
				splitBody[i] = word
				break
			}
		}
	}

	return strings.Join(splitBody, "")
}

func validateChirpHandler(response http.ResponseWriter, request *http.Request) {
	type parameters struct {
		Body string `json:"body"`
	}

	response.Header().Set("Content-Type", "application/json")
	decoder := json.NewDecoder(request.Body)
	params := parameters{}
	err := decoder.Decode(&params)
	if err != nil {
		log.Printf("Error decoding parameters: %s", err)
		response.WriteHeader(http.StatusInternalServerError)
		jsonData, _ := writeJsondataError("Something went wrong")
		response.Write(jsonData)
		return
	}

	if len(params.Body) > 140 {
		response.WriteHeader(http.StatusBadRequest)
		jsonData, err := writeJsondataError("Chirp is too long")
		if err != nil {
			log.Printf("Error Marshaling response body: %s", err)
			response.WriteHeader(http.StatusInternalServerError)
			jsonData, _ := writeJsondataError("Something went wrong")
			response.Write(jsonData)
			return
		}
		response.Write(jsonData)
		return
	}

	badWords := []string{"kerfuffle", "sharbert", "fornax"}
	censoredBody := removeProfanity(params.Body, badWords, "****")
	type returnVals struct {
		CleanedBody string `json:"cleaned_body"`
	}
	respBody := returnVals{
		CleanedBody: censoredBody,
	}
	jsonData, err := json.Marshal(respBody)
	if err != nil {
		log.Printf("Error marshaling valid response body: %s", err)
		response.Write([]byte("Error marshaling"))
	}
	response.WriteHeader(http.StatusOK)
	response.Write(jsonData)

}

func healthzHandler(responseWrite http.ResponseWriter, request *http.Request) {
	responseWrite.Header().Add("Content-Type", "text/plain; charset=utf-8")
	responseWrite.WriteHeader(http.StatusOK)

	var body = []byte("OK")
	responseWrite.Write(body)
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatalf("Error loading .env file: %s", err)
	}
	dbURL := os.Getenv("DB_URL")

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("Error connecting to database: %s ", err)
	}

	fileServer := http.StripPrefix("/app", http.FileServer(http.Dir(".")))

	apiConfig := apiConfig{
		fileserverHits: atomic.Int32{},
		dbQueries:      database.New(db),
		platform:       os.Getenv("PLATFORM"),
	}

	serveMux := http.NewServeMux()
	serveMux.Handle("/app/", apiConfig.middlewareMetricsInc(middlewareLog(fileServer)))
	serveMux.Handle("GET /api/healthz", middlewareLog(http.HandlerFunc(healthzHandler)))
	serveMux.Handle("POST /api/validate_chirp", middlewareLog(http.HandlerFunc(validateChirpHandler)))
	serveMux.Handle("GET /admin/metrics", middlewareLog(http.HandlerFunc(apiConfig.metricsHandler)))
	serveMux.Handle("POST /admin/reset", middlewareLog(http.HandlerFunc(apiConfig.resetHandler)))
	serveMux.Handle("POST /api/users", middlewareLog(http.HandlerFunc(apiConfig.createUserHandler)))

	server := http.Server{
		Addr:    ":8080",
		Handler: serveMux,
	}

	log.Fatal(server.ListenAndServe())
}
