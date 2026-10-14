package rfb

import (
	"bytes"
	"testing"
)

func TestEncryptPasswdReference(t *testing.T) {
	enc := EncryptPasswd("password")
	want := []byte{0xdb, 0xd8, 0x3c, 0xfd, 0x72, 0x7a, 0x14, 0x58}
	if !bytes.Equal(enc[:], want) {
		t.Fatalf("EncryptPasswd = %x, want %x", enc[:], want)
	}
}

func TestEncryptBytesReference(t *testing.T) {
	challenge := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
		0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00}
	vncEncryptBytes(challenge, "password")
	want := []byte{0x5d, 0x3b, 0x66, 0x79, 0x1a, 0x0a, 0x48, 0x10,
		0xc8, 0xd2, 0x74, 0xb7, 0x92, 0x49, 0xbe, 0x96}
	if !bytes.Equal(challenge, want) {
		t.Fatalf("vncEncryptBytes = %x, want %x", challenge, want)
	}
}

func TestVerifyChallengeRoundTrip(t *testing.T) {
	challenge := make([]byte, 16)
	for i := range challenge {
		challenge[i] = byte(i * 7 + 1)
	}
	resp := make([]byte, 16)
	copy(resp, challenge)
	vncEncryptBytes(resp, "secret")
	if !verifyChallenge(challenge, resp, "secret") {
		t.Fatal("verifyChallenge should pass for correct password")
	}
	if verifyChallenge(challenge, resp, "wrong") {
		t.Fatal("verifyChallenge should fail for wrong password")
	}
}