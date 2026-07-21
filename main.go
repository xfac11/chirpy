package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
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
	polkaAPIKey    string
}

type User struct {
	ID            uuid.UUID `json:"id"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Email         string    `json:"email"`
	Token         string    `json:"token"`
	RefreshToken  string    `json:"refresh_token"`
	Is_Chirpy_Red bool      `json:"is_chirpy_red"`
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
		ID:            dbUser.ID,
		CreatedAt:     dbUser.CreatedAt,
		UpdatedAt:     dbUser.UpdatedAt,
		Email:         dbUser.Email,
		Is_Chirpy_Red: dbUser.IsChirpyRed,
	}

	err = respondWithJson(response, http.StatusCreated, user)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not marshal to json encoding of main.User: %s", err), "Something went wrong")
		return
	}

	log.Printf("Successfully created a user with id: %s", user.ID)
}

func respondWithError(response http.ResponseWriter, statusCode int, logMsg, clientMsg string) {
	log.Printf(logMsg)
	type returnVals struct {
		Error string `json:"error"`
	}
	respBody := returnVals{
		Error: clientMsg,
	}
	respondWithJson(response, statusCode, respBody)
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

	user := User{
		ID:            dbUser.ID,
		CreatedAt:     dbUser.CreatedAt,
		UpdatedAt:     dbUser.UpdatedAt,
		Email:         dbUser.Email,
		Token:         signedToken,
		RefreshToken:  refreshToken,
		Is_Chirpy_Red: dbUser.IsChirpyRed,
	}

	err = respondWithJson(response, http.StatusOK, user)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not marshal to json encoding of main.User: %s", err), "Something went wrong on the server")
		return
	}

	log.Printf("Email and password matched. Sending user resource")
}

func validateChirpBody(body string) (string, error) {
	if len(body) > 140 {
		return "", fmt.Errorf("Chirp is too long")
	}

	badWords := []string{"kerfuffle", "sharbert", "fornax"}
	censoredBody := removeProfanity(body, badWords, "****")

	return censoredBody, nil
}

func (cfg *apiConfig) createChirpHandler(response http.ResponseWriter, request *http.Request) {
	type parameters struct {
		Body string `json:"body"`
	}

	userID, err := getUserIDFromAccessToken(request.Header, cfg.jwtSecret)
	if err != nil {
		respondWithError(response, http.StatusUnauthorized, fmt.Sprintf("Non valid access token : %s", err), "Unauthorized. Need a valid access token in the header")
		return
	}
	var params parameters
	decoder := json.NewDecoder(request.Body)
	err = decoder.Decode(&params)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not decode request body into a struct: %s", err), "Something went wrong")
		return
	}

	validatedBody, err := validateChirpBody(params.Body)
	if err != nil {
		respondWithError(response, http.StatusBadRequest, fmt.Sprintf("Error validating chrip body: %s", err), "Chirp is too long")
		return
	}

	createParams := database.CreateChirpParams{
		UserID: userID,
		Body:   validatedBody,
	}
	dbChirp, err := cfg.dbQueries.CreateChirp(request.Context(), createParams)
	if err != nil {
		respondWithError(response, http.StatusConflict, fmt.Sprintf("Error creating chirp : %s", err), "Something went wrong. Probably invalid user_id")
		return
	}

	chirp := makeChirpFromDBChirp(dbChirp)

	err = respondWithJson(response, http.StatusCreated, chirp)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not marshal to json encoding of User: %s", err), "Something went wrong")
		return
	}

	log.Printf("Successfully created a chirp with id: %s", chirp.ID)
}

func makeChirpFromDBChirp(dbChirp database.Chirp) Chirp {
	return Chirp{
		ID:        dbChirp.ID,
		CreatedAt: dbChirp.CreatedAt,
		UpdatedAt: dbChirp.UpdatedAt,
		Body:      dbChirp.Body,
		User_ID:   dbChirp.UserID,
	}
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

	chirp := makeChirpFromDBChirp(chirpDB)

	err = respondWithJson(response, http.StatusOK, chirp)
	if err != nil {
		respondWithError(response, http.StatusNotFound, fmt.Sprintf("Could not marshal a chirp to json chrip : %s", err), "Something went wrong when serialising response body")
		return
	}

	log.Printf("Successfully sent one chirp")
}

// Sending all chirps with available query parameters:
//
// author_id : only send chirps belonging to this user/author
//
// sort: Use 'asc' or 'desc' to sort the chirps in ascending or descending order. asc is default
func (cfg *apiConfig) getAllChirpsHandler(response http.ResponseWriter, request *http.Request) {
	err := request.ParseForm()
	if err != nil {
		respondWithError(response, http.StatusBadRequest, fmt.Sprintf("Could not parse the request form: %s", err), "Something went wrong when parsing the request.")
		return
	}
	var dbChirps []database.Chirp
	if request.Form.Has("author_id") {
		author_id := request.Form.Get("author_id")
		user_uuid, err := uuid.Parse(author_id)
		if err != nil {
			respondWithError(response, http.StatusNotFound, fmt.Sprintf("The author id could not be made into a uuid: %s", err), "Need a valid author/user id")
			return
		}
		dbChirps, err = cfg.dbQueries.GetAllChripsFromAuthor(request.Context(), user_uuid)
		if err != nil {
			respondWithError(response, http.StatusNotFound, fmt.Sprintf("Could not get all chirps from the user in the database: %s", err), "Something went wrong when querying the chirps for that user. Probably wrong id")
			return
		}
	} else {
		dbChirps, err = cfg.dbQueries.GetAllChirps(request.Context())
		if err != nil {
			respondWithError(response, http.StatusNotFound, fmt.Sprintf("Could not get all chirps from the database: %s", err), "Something went wrong when querying all chirps")
			return
		}
	}

	chirps := make([]Chirp, 0, len(dbChirps))
	for _, dbChirp := range dbChirps {
		chirps = append(chirps, makeChirpFromDBChirp(dbChirp))
	}

	var sortOrder string
	if request.Form.Has("sort") {
		sortOrder = request.Form.Get("sort")
	}

	if sortOrder == "desc" {
		slices.SortFunc(chirps, func(a, b Chirp) int {
			if a.CreatedAt.Before(b.CreatedAt) {
				return 1
			} else if a.CreatedAt.After(b.CreatedAt) {
				return -1
			} else {
				return 0
			}
		})
	}

	err = respondWithJson(response, http.StatusOK, chirps)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not marshal chirps into jsonchirps : %s", err), "Something went wrong")
		return
	}

	log.Printf("Retrieved all chirps and sending them")

}
func respondWithJson(responseWriter http.ResponseWriter, statusCode int, v any) error {
	jsonData, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("Could not marshal/serialize v: %s", err)
	}
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	responseWriter.Write(jsonData)
	return nil
}
func getRefreshToken(header map[string][]string, queries *database.Queries, ctx context.Context) (database.RefreshToken, error) {
	bearerToken, err := auth.GetBearerToken(header)
	if err != nil {
		return database.RefreshToken{}, fmt.Errorf("No bearer token found : %s", err)
	}

	refreshToken, err := queries.GetRefreshToken(ctx, bearerToken)
	if err != nil {
		return database.RefreshToken{}, fmt.Errorf("No refresh token found in db : %s", err)
	}
	return refreshToken, nil
}

func validateRefreshToken(refreshToken database.RefreshToken) bool {
	return refreshToken.RevokedAt.Valid || refreshToken.ExpiresAt.Before(time.Now())
}

func (cfg *apiConfig) refreshHandler(response http.ResponseWriter, request *http.Request) {
	refreshToken, err := getRefreshToken(request.Header, cfg.dbQueries, request.Context())
	if err != nil {
		respondWithError(response, http.StatusUnauthorized, fmt.Sprintf("No refresh token: %s", err), "Refresh token not found")
		return
	}
	if validateRefreshToken(refreshToken) {
		respondWithError(response, http.StatusUnauthorized, "Refresh token expired or revoked", "Refresh token expired or revoked")
		return
	}

	type Body struct {
		Token string `json:"token"`
	}
	user, err := cfg.dbQueries.GetUserFromRefreshToken(request.Context(), refreshToken.Token)
	if err != nil {
		respondWithError(response, http.StatusUnauthorized, fmt.Sprintf("Could not find a user related to that refresh token : %s", err), "Not valid refresh token")
		return
	}

	expirationTime, _ := time.ParseDuration("1h")
	accessToken, err := auth.MakeJWT(user.ID, cfg.jwtSecret, expirationTime)
	if err != nil {
		respondWithError(response, http.StatusUnauthorized, fmt.Sprintf("Could not create access token : %s", err), "Something went wrong when creating access token")
		return
	}

	body := Body{
		Token: accessToken,
	}

	err = respondWithJson(response, http.StatusOK, body)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not marshal a body to json body : %s", err), "Something went wrong serialising/marshaling response body")
		return
	}

	log.Printf("Successfully refreshed token")
}

func (cfg *apiConfig) revokeHandler(response http.ResponseWriter, request *http.Request) {
	refreshToken, err := getRefreshToken(request.Header, cfg.dbQueries, request.Context())
	if err != nil {
		respondWithError(response, http.StatusUnauthorized, fmt.Sprintf("No refresh token: %s", err), "Refresh token not found")
		return
	}

	err = cfg.dbQueries.UpdateRefreshToken(request.Context(), refreshToken.Token)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not revoke refresh token in db : %s", err), "Something went wrong when revoking refresh token")
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

	userID, err := getUserIDFromAccessToken(request.Header, cfg.jwtSecret)
	if err != nil {
		respondWithError(response, http.StatusUnauthorized, fmt.Sprintf("Non valid access token : %s", err), "Unauthorized. Need a valid access token in the header")
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
		ID:            updatedUser.ID,
		CreatedAt:     updatedUser.CreatedAt,
		UpdatedAt:     updatedUser.UpdatedAt,
		Email:         updatedUser.Email,
		Is_Chirpy_Red: updatedUser.IsChirpyRed,
	}

	err = respondWithJson(response, http.StatusOK, userResource)
	if err != nil {
		respondWithError(response, http.StatusInternalServerError, fmt.Sprintf("Could not marshal updated user to user resource : %s", err), "Something went wrong")
		return
	}
	log.Printf("Successfully updated user email and password")

}

// Gets the userID by getting the access token from the Authorization header with the prefix 'Bearer' and validate it with the secret.
//
// header structure:
//
// Authorization: "Bearer [token]"
func getUserIDFromAccessToken(header map[string][]string, secret string) (uuid.UUID, error) {
	bearerToken, err := auth.GetBearerToken(header)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("Could not find a token in the header : %s", err)
	}

	userID, err := auth.ValidateJWT(bearerToken, secret)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("Non valid bearer token : %s", err)
	}
	return userID, nil
}

func (cfg *apiConfig) deleteChirpByIDHandler(response http.ResponseWriter, request *http.Request) {
	chirpID := request.PathValue("chirpID")
	if len(chirpID) == 0 {
		respondWithError(response, http.StatusBadRequest, "Could not retrieve chirp beacuse it needs an id", "Need a chirp id")
		return
	}

	userID, err := getUserIDFromAccessToken(request.Header, cfg.jwtSecret)
	if err != nil {
		respondWithError(response, http.StatusUnauthorized, fmt.Sprintf("Non valid access token : %s", err), "Unauthorized. Need a valid access token in the header")
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

func (cfg *apiConfig) polkaWebhookHandler(response http.ResponseWriter, request *http.Request) {
	type polkaWebhookRequest struct {
		Event string `json:"event"`
		Data  struct {
			UserID uuid.UUID `json:"user_id"`
		} `json:"data"`
	}

	apiKey, err := auth.GetAPIKey(request.Header)
	if err != nil {
		respondWithError(response, http.StatusUnauthorized, fmt.Sprintf("No authirization header with an api key found: %s", err), "Unauthorized, need a valid api key")
		return
	}
	if apiKey != cfg.polkaAPIKey {
		respondWithError(response, http.StatusUnauthorized, "API key found but not valid polka key. Non equal to servers api key", "Unauthorized, need a valid api key")
		return
	}

	var polkaRequest polkaWebhookRequest
	decoder := json.NewDecoder(request.Body)
	err = decoder.Decode(&polkaRequest)
	if err != nil {
		respondWithError(response, http.StatusBadRequest, fmt.Sprintf("Could not decode to polkaWebhookRequest: %s", err), "Something was wrong with the request body")
		return
	}

	if polkaRequest.Event != "user.upgraded" {
		response.WriteHeader(http.StatusNoContent)
		return
	}

	err = cfg.dbQueries.UpgradeUserToChirpyRed(context.Background(), polkaRequest.Data.UserID)
	if err != nil {
		respondWithError(response, http.StatusNotFound, fmt.Sprintf("Could not upgrade user. Probably not found: %s", err), "User cant be found")
		return
	}

	response.WriteHeader(http.StatusNoContent)

}

// Prints out the method and the url path before serving the handler
func middlewareLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
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
		polkaAPIKey:    os.Getenv("POLKA_KEY"),
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
	serveMux.Handle("POST /api/polka/webhooks", middlewareLog(http.HandlerFunc(apiConfig.polkaWebhookHandler)))
	server := http.Server{
		Addr:    ":8080",
		Handler: serveMux,
	}

	log.Fatal(server.ListenAndServe())
}
