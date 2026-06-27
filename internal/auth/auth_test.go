package auth

import "testing"

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
