package indexer

import (
	"testing"
)

func TestComputeASTHash_Go(t *testing.T) {
	source := []byte(`package main

func Hello() {
	x := 1
	fmt.Println(x)
}
`)

	hash1, err := computeASTHash(source, "go")
	if err != nil {
		t.Fatalf("computeASTHash failed: %v", err)
	}
	if hash1 == "" {
		t.Fatal("expected non-empty hash")
	}

	// Adding a comment should NOT change the structural hash
	sourceWithComment := []byte(`package main

// This is a new comment
func Hello() {
	x := 1
	fmt.Println(x)
}
`)
	hash2, err := computeASTHash(sourceWithComment, "go")
	if err != nil {
		t.Fatalf("computeASTHash with comment failed: %v", err)
	}
	if hash1 != hash2 {
		t.Errorf("adding a comment changed the hash: %s != %s", hash1, hash2)
	}

	// Renaming a variable SHOULD change the hash
	sourceRenamed := []byte(`package main

func Hello() {
	y := 1
	fmt.Println(y)
}
`)
	hash3, err := computeASTHash(sourceRenamed, "go")
	if err != nil {
		t.Fatalf("computeASTHash with renamed var failed: %v", err)
	}
	if hash1 == hash3 {
		t.Error("renaming a variable should change the hash")
	}
}

func TestComputeASTHash_Python(t *testing.T) {
	source := []byte(`def hello():
    x = 1
    print(x)
`)
	hash1, err := computeASTHash(source, "python")
	if err != nil {
		t.Fatalf("computeASTHash python failed: %v", err)
	}

	// Comment change
	sourceComment := []byte(`# new comment
def hello():
    x = 1
    print(x)
`)
	hash2, err := computeASTHash(sourceComment, "python")
	if err != nil {
		t.Fatalf("computeASTHash python with comment failed: %v", err)
	}
	if hash1 != hash2 {
		t.Errorf("adding a comment changed the python hash: %s != %s", hash1, hash2)
	}
}

func TestComputeASTHash_UnsupportedLanguage(t *testing.T) {
	_, err := computeASTHash([]byte("hello"), "rust")
	if err == nil {
		t.Error("expected error for unsupported language")
	}
}
