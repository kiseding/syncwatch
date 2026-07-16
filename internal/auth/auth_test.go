package auth

import "testing"

func TestPasswordHashAndTokenRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword("correct horse", hash)
	if err != nil || !ok {
		t.Fatalf("valid password rejected: ok=%v err=%v", ok, err)
	}
	if ok, _ := VerifyPassword("wrong", hash); ok {
		t.Fatal("invalid password accepted")
	}

	tm := NewTokenManager("test-secret", 60)
	token, err := tm.Generate("host")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := tm.Validate(token)
	if err != nil || claims.Role != "host" {
		t.Fatalf("token round trip failed: claims=%#v err=%v", claims, err)
	}
}

func TestRateLimiterHandlesInvalidConfiguredRate(t *testing.T) {
	rl := NewRateLimiter()
	defer rl.Stop()
	if !rl.Allow("client", 0) {
		t.Fatal("first request should be allowed with a default rate")
	}
}
