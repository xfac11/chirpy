package auth

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCreateHash(t *testing.T) {
	_, err := HashPassword("pass%or#")
	if err != nil {
		t.Errorf("Error creating a hash from a string password : %s", err)
	}

}

func TestCompareHash(t *testing.T) {
	hash, err := HashPassword("logg4n")
	if err != nil {
		t.Errorf("Error creating a hash from a string password : %s", err)
	}
	match, err := CheckPasswordHash("logg4n", hash)
	if err != nil || match == false {
		t.Errorf("Error checking password with hashed password : %s", err)
	}
}

func TestShouldNotMatch(t *testing.T) {
	hash, err := HashPassword("word1234")
	if err != nil {
		t.Errorf("Error creating a hash from a string password : %s", err)
	}

	match, err := CheckPasswordHash("letter1234", hash)
	if err != nil {
		t.Errorf("Error checking password with hashed password : %s", err)
	}

	if match == true {
		t.Errorf("Should not match letter1234 with hashed word1234")
	}
}

func TestMakingJWTToken(t *testing.T) {
	id := uuid.New()
	mySigningKey := []byte("AllYourBase")
	expiresIn, _ := time.ParseDuration("1s")

	signedToken, err := MakeJWT(id, string(mySigningKey), expiresIn)
	if err != nil {
		t.Errorf("Could not Make a JWT : %s", err)
	}

	validID, err := ValidateJWT(signedToken, string(mySigningKey))
	if err != nil {
		t.Errorf("Should validate correctly : %s", err)
	}

	if validID != id {
		t.Errorf("valid id and id should be the same")
	}
}

func TestExpiredToken(t *testing.T) {
	id := uuid.New()
	mySigningKey := []byte("AllYourBase")
	expiresIn, _ := time.ParseDuration("1s")

	signedToken, err := MakeJWT(id, string(mySigningKey), expiresIn)
	if err != nil {
		t.Errorf("Could not Make a JWT : %s", err)
	}

	time.Sleep(time.Duration(time.Second * 2))

	_, err = ValidateJWT(signedToken, string(mySigningKey))
	if err == nil {
		t.Errorf("Should be rejected beacuse it is expired")
	}

}

func TestWrongSecretToken(t *testing.T) {
	id := uuid.New()
	mySigningKey := []byte("AllYourBase")
	expiresIn, _ := time.ParseDuration("1s")

	signedToken, err := MakeJWT(id, string(mySigningKey), expiresIn)
	if err != nil {
		t.Errorf("Could not Make a JWT : %s", err)
	}

	_, err = ValidateJWT(signedToken, "NoneOfYourBase")
	if err == nil {
		t.Errorf("Should be rejected because of wrong secret")
	}
}

func TestAuthHeader(t *testing.T) {
	header := http.Header{}
	header.Add("Authorization", "Bearer 231mkjnasd82")
	token, err := GetBearerToken(header)
	if err != nil {
		t.Errorf("Could not find token : %s", err)
	}

	if token != "231mkjnasd82" {
		t.Errorf("Token is not the same. It should be 231mkjnasd82 but is %s", token)
	}
}

func TestAuthHeaderInvalidSyntax(t *testing.T) {
	header := http.Header{}
	header.Add("Authorization", "Token 231mkjnasd82")
	_, err := GetBearerToken(header)
	if err == nil {
		t.Error("Should error cause wrong syntax in the Authorization header")
	}
}
