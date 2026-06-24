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

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/xfac11/chirpy/internal/database"
)

type apiConfig struct {
	fileserverHits atomic.Int32
	dbQueries      *database.Queries
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
	cfg.fileserverHits.Store(0)

	responeWrite.Header().Set("Content-Type", "text/plain; charset=utf-8")
	responeWrite.WriteHeader(http.StatusOK)
	var body = []byte("OK")
	responeWrite.Write(body)
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
	}

	serveMux := http.NewServeMux()
	serveMux.Handle("/app/", apiConfig.middlewareMetricsInc(middlewareLog(fileServer)))
	serveMux.Handle("GET /api/healthz", middlewareLog(http.HandlerFunc(healthzHandler)))
	serveMux.Handle("POST /api/validate_chirp", middlewareLog(http.HandlerFunc(validateChirpHandler)))
	serveMux.Handle("GET /admin/metrics", middlewareLog(http.HandlerFunc(apiConfig.metricsHandler)))
	serveMux.Handle("POST /admin/reset", middlewareLog(http.HandlerFunc(apiConfig.resetHandler)))

	server := http.Server{
		Addr:    ":8080",
		Handler: serveMux,
	}

	log.Fatal(server.ListenAndServe())
}
