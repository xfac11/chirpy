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
	"github.com/xfac11/chirpy/internal/auth"
	"github.com/xfac11/chirpy/internal/database"
)

type apiConfig struct {
	fileserverHits atomic.Int32
	dbQueries      *database.Queries
	platform       string
	jwtSecret      string
}

type User struct {
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Email     string    `json:"email"`
}

type Chirp struct {
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Body      string    `json:"body"`
	User_ID   uuid.UUID `json:"user_id"`
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
		respondWithError(responeWrite, http.StatusForbidden, "Wrong platform", "Forbidden request")
		return
	}

	cfg.fileserverHits.Store(0)

	err := cfg.dbQueries.DeleteAllUsers(request.Context())
	if err != nil {
		respondWithError(responeWrite, http.StatusInternalServerError, fmt.Sprintf("Could not delete all users : %s", err), "Something went wrong when deleting all users")
		return
	}

	var body = []byte("OK")
	responeWrite.Header().Set("Content-Type", "text/plain; charset=utf-8")
	responeWrite.WriteHeader(http.StatusOK)
	responeWrite.Write(body)
}

func (cfg *apiConfig) createUserHandler(response http.ResponseWriter, request *http.Request) {
	type parameters struct {
		Password string `json:"password"`
		Email    string `json:"email"`
	}
	params := parameters{}
	decoder := json.NewDecoder(request.Body)
	err := decoder.Decode(&params)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not decode request body into a struct: %s", err), "Something went wrong")
		return
	}

	hashedPassword, err := auth.HashPassword(params.Password)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not create a hash from that password : %s", err), "Something went wrong")
		return
	}
	createUserParam := database.CreateUserParams{
		Email:          params.Email,
		HashedPassword: hashedPassword,
	}
	dbUser, err := cfg.dbQueries.CreateUser(request.Context(), createUserParam)
	if err != nil {
		respondWithError(response, http.StatusConflict, fmt.Sprintf("Could not create a user using email: %s, error: %s", params.Email, err), "A user with that email already exists")
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
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not marshal to json encoding of main.User: %s", err), "Something went wrong")
		return
	}

	log.Printf("Successfully created a user with id: %s", user.ID)
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusCreated)
	response.Write(jsonUser)
}

func respondWithError(response http.ResponseWriter, statusCode int, logMsg, clientMsg string) {
	log.Printf(logMsg)
	errorMsg, _ := writeJsondataError(clientMsg)
	response.Header().Set("Content-Type", "json/application")
	response.WriteHeader(statusCode)
	response.Write(errorMsg)
}

func (cfg *apiConfig) loginHandler(response http.ResponseWriter, request *http.Request) {
	type parameters struct {
		Password string `json:"password"`
		Email    string `json:"email"`
	}

	var params parameters
	decoder := json.NewDecoder(request.Body)
	err := decoder.Decode(&params)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not decode request body into a struct: %s", err), "Something went wrong")
		return
	}

	dbUser, err := cfg.dbQueries.GetUserByEmail(request.Context(), params.Email)
	if err != nil {
		respondWithError(response, http.StatusUnauthorized, fmt.Sprintf("Could not found a user with the email: %s. Error : %s", params.Email, err), "Incorrect email or password")
		return
	}

	match, err := auth.CheckPasswordHash(params.Password, dbUser.HashedPassword)
	if err != nil || match == false {
		respondWithError(response, http.StatusUnauthorized, fmt.Sprintf("Could not match password. Error : %s", err), "Incorrect email or password")
		return
	}

	tokenExpirationTime, err := time.ParseDuration("1h")
	signedToken, err := auth.MakeJWT(dbUser.ID, cfg.jwtSecret, tokenExpirationTime)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not create a signed token : %s", err), "Something went wrong")
		return
	}

	refreshTokenExpirationTime, _ := time.ParseDuration("1440h")
	refreshToken := auth.MakeRefreshToken()
	createRTokenParams := database.CreateRefreshTokenParams{
		Token:     refreshToken,
		UserID:    dbUser.ID,
		ExpiresAt: time.Now().Add(refreshTokenExpirationTime),
	}
	dbRefreshToken, err := cfg.dbQueries.CreateRefreshToken(request.Context(), createRTokenParams)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not create a refresh token inside db: %s", err), "Something went wrong")
		return
	}
	log.Printf("Created a refresh token inside database at %s", dbRefreshToken.CreatedAt)
	type Body struct {
		ID           uuid.UUID `json:"id"`
		CreatedAt    time.Time `json:"created_at"`
		UpdatedAt    time.Time `json:"updated_at"`
		Email        string    `json:"email"`
		Token        string    `json:"token"`
		RefreshToken string    `json:"refresh_token"`
	}

	body := Body{
		ID:           dbUser.ID,
		CreatedAt:    dbUser.CreatedAt,
		UpdatedAt:    dbUser.UpdatedAt,
		Email:        dbUser.Email,
		Token:        signedToken,
		RefreshToken: refreshToken,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not marshal to json encoding of main.User: %s", err), "Something went wrong on the server")
		return
	}

	log.Printf("Email and password matched. Sending user resource")
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	response.Write(jsonBody)
}

func (cfg *apiConfig) createChirpHandler(response http.ResponseWriter, request *http.Request) {
	type parameters struct {
		Body string `json:"body"`
	}

	bearerToken, err := auth.GetBearerToken(request.Header)
	if err != nil {
		respondWithError(response, http.StatusUnauthorized, fmt.Sprintf("No bearer token in request header : %s", err), "Unauthorized")
		return

	}
	userID, err := auth.ValidateJWT(bearerToken, cfg.jwtSecret)
	if err != nil {
		respondWithError(response, http.StatusUnauthorized, fmt.Sprintf("Non valid bearer token : %s", err), "Unauthorized")
		return
	}
	var params parameters
	decoder := json.NewDecoder(request.Body)
	err = decoder.Decode(&params)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not decode request body into a struct: %s", err), "Something went wrong")
		return
	}

	if len(params.Body) > 140 {
		respondWithError(response, http.StatusBadRequest, fmt.Sprintf("Error Marshaling response body: %s", err), "Chirp is too long")
		return
	}

	badWords := []string{"kerfuffle", "sharbert", "fornax"}
	censoredBody := removeProfanity(params.Body, badWords, "****")

	createParams := database.CreateChirpParams{
		UserID: userID,
		Body:   censoredBody,
	}
	dbChirp, err := cfg.dbQueries.CreateChirp(request.Context(), createParams)
	if err != nil {
		respondWithError(response, http.StatusConflict, fmt.Sprintf("Error creating chirp : %s", err), "Something went wrong. Probably invalid user_id")
		return
	}

	chirp := Chirp{
		ID:        dbChirp.ID,
		CreatedAt: dbChirp.CreatedAt,
		UpdatedAt: dbChirp.UpdatedAt,
		Body:      dbChirp.Body,
		User_ID:   dbChirp.UserID,
	}

	jsonChirp, err := json.Marshal(chirp)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not marshal to json encoding of User: %s", err), "Something went wrong")
		return
	}

	log.Printf("Successfully created a chirp with id: %s", chirp.ID)
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusCreated)
	response.Write(jsonChirp)

}

func (cfg *apiConfig) getChirpHandler(response http.ResponseWriter, request *http.Request) {
	chirpID := request.PathValue("chirpID")
	if len(chirpID) == 0 {
		respondWithError(response, http.StatusNotFound, "Could not retrieve chirp because it needs an id", "Need a chirp id in the path")
		return
	}

	chirpUUID, err := uuid.Parse(chirpID)
	if err != nil {
		respondWithError(response, http.StatusNotFound, fmt.Sprintf("Could not create a UUID from the pathvalue 'chirpID' : %s", err), "Invalid chirp id")
		return
	}

	chirpDB, err := cfg.dbQueries.GetChirp(request.Context(), chirpUUID)
	if err != nil {
		respondWithError(response, http.StatusNotFound, fmt.Sprintf("Could not retrieve a chirp from the database. Faulty id : %s", err), "Invalid chirp id")
		return
	}

	chirp := Chirp{
		ID:        chirpDB.ID,
		CreatedAt: chirpDB.CreatedAt,
		UpdatedAt: chirpDB.UpdatedAt,
		Body:      chirpDB.Body,
		User_ID:   chirpDB.UserID,
	}

	chirpJson, err := json.Marshal(chirp)
	if err != nil {
		respondWithError(response, http.StatusNotFound, fmt.Sprintf("Could not marshal a chirp to json chrip : %s", err), "Something went wrong when serialising response body")
		return
	}

	log.Printf("Successfully sent one chirp")
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	response.Write(chirpJson)
}

func (cfg *apiConfig) getAllChirpsHandler(response http.ResponseWriter, request *http.Request) {
	dbChirps, err := cfg.dbQueries.GetAllChirps(request.Context())
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Error retreiving all chirps : %s", err), "Something went wrong when getting all chirps from the database")
		return
	}

	chirps := make([]Chirp, 0, len(dbChirps))
	for _, dbChirp := range dbChirps {
		chirps = append(chirps, Chirp{
			ID:        dbChirp.ID,
			CreatedAt: dbChirp.CreatedAt,
			UpdatedAt: dbChirp.UpdatedAt,
			Body:      dbChirp.Body,
			User_ID:   dbChirp.UserID,
		})
	}

	jsonChirps, err := json.Marshal(chirps)
	if err != nil {
		log.Printf("Could not marshal chirps into jsonchirps : %s", err)
		errorMsg, _ := writeJsondataError("Something went wrong")
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusInternalServerError)
		response.Write(errorMsg)
		return
	}

	log.Printf("Retrieved all chirps and sending them")
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	response.Write(jsonChirps)

}

func (cfg *apiConfig) refreshHandler(response http.ResponseWriter, request *http.Request) {
	bearerToken, err := auth.GetBearerToken(request.Header)
	if err != nil {
		log.Printf("No bearer token found : %s", err)
		errorMsg, _ := writeJsondataError("Refresh token required in the header")
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusUnauthorized)
		response.Write(errorMsg)
		return
	}

	refreshToken, err := cfg.dbQueries.GetRefreshToken(request.Context(), bearerToken)
	if err != nil {
		log.Printf("No refresh token found in db : %s", err)
		errorMsg, _ := writeJsondataError("Refresh token not present in the database")
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusUnauthorized)
		response.Write(errorMsg)
		return
	}
	if refreshToken.RevokedAt.Valid || refreshToken.ExpiresAt.Before(time.Now()) {
		log.Printf("Refresh token expired or revoked")
		errorMsg, _ := writeJsondataError("Refresh token expired or revoked")
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusUnauthorized)
		response.Write(errorMsg)
		return
	}

	type Body struct {
		Token string `json:"token"`
	}
	user, err := cfg.dbQueries.GetUserFromRefreshToken(request.Context(), refreshToken.Token)
	if err != nil {
		log.Printf("Could not find a user related to that refresh token : %s", err)
		errorMsg, _ := writeJsondataError("Not valid refresh token")
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusUnauthorized)
		response.Write(errorMsg)
		return
	}

	expirationTime, _ := time.ParseDuration("1h")
	accessToken, err := auth.MakeJWT(user.ID, cfg.jwtSecret, expirationTime)
	if err != nil {
		log.Printf("Could not create access token : %s", err)
		errorMsg, _ := writeJsondataError("Something went wrong")
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusUnauthorized)
		response.Write(errorMsg)
		return
	}

	body := Body{
		Token: accessToken,
	}

	bodyJson, err := json.Marshal(body)
	if err != nil {
		log.Printf("Could not marshal a body to json body : %s", err)
		errorMsg, _ := writeJsondataError("Something went wrong")
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusInternalServerError)
		response.Write(errorMsg)
		return
	}

	log.Printf("Successfully refreshed token")
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	response.Write(bodyJson)
}

func (cfg *apiConfig) revokeHandler(response http.ResponseWriter, request *http.Request) {
	bearerToken, err := auth.GetBearerToken(request.Header)
	if err != nil {
		log.Printf("No bearer token found : %s", err)
		errorMsg, _ := writeJsondataError("Refresh token required in the header")
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusUnauthorized)
		response.Write(errorMsg)
		return
	}

	refreshToken, err := cfg.dbQueries.GetRefreshToken(request.Context(), bearerToken)
	if err != nil {
		log.Printf("No refresh token found in db : %s", err)
		errorMsg, _ := writeJsondataError("Refresh token not present in the database")
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusUnauthorized)
		response.Write(errorMsg)
		return
	}

	err = cfg.dbQueries.UpdateRefreshToken(request.Context(), refreshToken.Token)
	if err != nil {
		log.Printf("Could not revoke refresh token in db : %s", err)
		errorMsg, _ := writeJsondataError("Something went wrong when revoking")
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusInternalServerError)
		response.Write(errorMsg)
		return
	}

	log.Printf("Successfully revoked refresh token")
	response.WriteHeader(http.StatusNoContent)
}

func (cfg *apiConfig) updateUserHandler(response http.ResponseWriter, request *http.Request) {
	type parameters struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	var params parameters
	decoder := json.NewDecoder(request.Body)
	err := decoder.Decode(&params)
	if err != nil {
		respondWithError(response, http.StatusBadRequest, fmt.Sprintf("Could not decode request body to a parameters : %s", err), "Wrong parameters inside of body. Need new email and new password")
		return
	}

	bearerToken, err := auth.GetBearerToken(request.Header)
	if err != nil {
		respondWithError(response, http.StatusUnauthorized, fmt.Sprintf("No bearer token found : %s", err), "Access token required in the header")
		return
	}

	userID, err := auth.ValidateJWT(bearerToken, cfg.jwtSecret)
	if err != nil {
		respondWithError(response, http.StatusUnauthorized, fmt.Sprintf("Invalid access token : %s", err), "Invalid access token")
		return
	}

	hashedPassword, err := auth.HashPassword(params.Password)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not create a hash from that password : %s", err), "Something went wrong")
		return
	}

	dbParams := database.SetUserPasswordAndEmailParams{
		ID:             userID,
		HashedPassword: hashedPassword,
		Email:          params.Email,
	}
	updatedUser, err := cfg.dbQueries.SetUserPasswordAndEmail(request.Context(), dbParams)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not update users' email and password : %s", err), "Something went wrong when updating the user")
		return
	}

	userResource := User{
		ID:        updatedUser.ID,
		CreatedAt: updatedUser.CreatedAt,
		UpdatedAt: updatedUser.UpdatedAt,
		Email:     updatedUser.Email,
	}
	userData, err := json.Marshal(userResource)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not marshal updated user to user resource : %s", err), "Something went wrong")
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	response.Write(userData)
	log.Printf("Successfully updated user email and password")

}

func (cfg *apiConfig) deleteChirpByIDHandler(response http.ResponseWriter, request *http.Request) {
	chirpID := request.PathValue("chirpID")
	if len(chirpID) == 0 {
		respondWithError(response, http.StatusBadRequest, "Could not retrieve chirp beacuse it needs an id", "Need a chirp id")
		return
	}

	bearerToken, err := auth.GetBearerToken(request.Header)
	if err != nil {
		respondWithError(response, http.StatusUnauthorized, fmt.Sprintf("Could not find a token in the header : %s", err), "Need a token in the header")
		return
	}

	userID, err := auth.ValidateJWT(bearerToken, cfg.jwtSecret)
	if err != nil {
		respondWithError(response, http.StatusUnauthorized, fmt.Sprintf("Non valid bearer token : %s", err), "Unauthorized")
		return
	}

	chirp, err := cfg.dbQueries.GetChirp(request.Context(), uuid.MustParse(chirpID))
	if err != nil {
		respondWithError(response, http.StatusNotFound, fmt.Sprintf("Could not retrieve chirp from database : %s", err), "No chirp with that id found")
		return
	}

	if chirp.UserID != userID {
		respondWithError(response, http.StatusForbidden, "The user is not the owner of the chirp", "Not the owner of the chirp")
		return
	}

	err = cfg.dbQueries.DeleteChirp(request.Context(), chirp.ID)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("The chirp could not be deleted : %s", err), "Something went wrong when deleting the chirp")
		return
	}

	log.Printf("The chirp with id: %s was deleted by the author: %s", chirp.ID, userID)
	response.WriteHeader(http.StatusNoContent)
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

	return strings.Join(splitBody, " ")
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
	jwtSecret := os.Getenv("JWT_SECRET")
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("Error connecting to database: %s ", err)
	}

	fileServer := http.StripPrefix("/app", http.FileServer(http.Dir(".")))

	apiConfig := apiConfig{
		fileserverHits: atomic.Int32{},
		dbQueries:      database.New(db),
		platform:       os.Getenv("PLATFORM"),
		jwtSecret:      jwtSecret,
	}

	serveMux := http.NewServeMux()
	serveMux.Handle("/app/", apiConfig.middlewareMetricsInc(middlewareLog(fileServer)))
	serveMux.Handle("GET /api/healthz", middlewareLog(http.HandlerFunc(healthzHandler)))
	serveMux.Handle("GET /admin/metrics", middlewareLog(http.HandlerFunc(apiConfig.metricsHandler)))
	serveMux.Handle("GET /api/chirps", middlewareLog(http.HandlerFunc(apiConfig.getAllChirpsHandler)))
	serveMux.Handle("POST /admin/reset", middlewareLog(http.HandlerFunc(apiConfig.resetHandler)))
	serveMux.Handle("POST /api/users", middlewareLog(http.HandlerFunc(apiConfig.createUserHandler)))
	serveMux.Handle("POST /api/chirps", middlewareLog(http.HandlerFunc(apiConfig.createChirpHandler)))
	serveMux.Handle("GET /api/chirps/{chirpID}", middlewareLog(http.HandlerFunc(apiConfig.getChirpHandler)))
	serveMux.Handle("POST /api/login", middlewareLog(http.HandlerFunc(apiConfig.loginHandler)))
	serveMux.Handle("POST /api/refresh", middlewareLog(http.HandlerFunc(apiConfig.refreshHandler)))
	serveMux.Handle("POST /api/revoke", middlewareLog(http.HandlerFunc(apiConfig.revokeHandler)))
	serveMux.Handle("PUT /api/users", middlewareLog(http.HandlerFunc(apiConfig.updateUserHandler)))
	serveMux.Handle("DELETE /api/chirps/{chirpID}", middlewareLog(http.HandlerFunc(apiConfig.deleteChirpByIDHandler)))
	server := http.Server{
		Addr:    ":8080",
		Handler: serveMux,
	}

	log.Fatal(server.ListenAndServe())
}
