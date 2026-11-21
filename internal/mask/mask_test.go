package mask

import (
	"strings"
	"testing"
)

func TestHeaderMasksSensitiveOnly(t *testing.T) {
	m := New(nil)
	if got := m.Header("Authorization", "Bearer abc"); got != Placeholder {
		t.Fatalf("Authorization = %q, want placeholder", got)
	}
	if got := m.Header("Content-Type", "application/json"); got != "application/json" {
		t.Fatalf("Content-Type changed to %q", got)
	}
}

func TestJSONMasksNestedFieldsAndKeepsNumbers(t *testing.T) {
	m := New(nil)
	in := `{"id":1,"price":299.0,"user":{"email":"a@b.com","tags":[{"token":"xyz"}]}}`
	out, ok := m.JSON([]byte(in))
	if !ok {
		t.Fatal("expected valid json")
	}
	s := string(out)
	if strings.Contains(s, "a@b.com") || strings.Contains(s, "xyz") {
		t.Fatalf("secret left in output: %s", s)
	}
	if !strings.Contains(s, `"price":299.0`) {
		t.Fatalf("number text changed: %s", s)
	}
}

func TestJSONRejectsInvalid(t *testing.T) {
	if _, ok := New(nil).JSON([]byte("not json")); ok {
		t.Fatal("invalid json should not be accepted")
	}
}

func TestExtraFieldsAreMasked(t *testing.T) {
	out, _ := New([]string{"Nickname"}).JSON([]byte(`{"nickname":"bob","city":"Delhi"}`))
	if strings.Contains(string(out), "bob") || !strings.Contains(string(out), "Delhi") {
		t.Fatalf("unexpected masking result: %s", out)
	}
}

func TestQuery(t *testing.T) {
	got := New(nil).Query("category=audio&token=abc&page=2")
	want := "category=audio&token=" + Placeholder + "&page=2"
	if got != want {
		t.Fatalf("Query = %q, want %q", got, want)
	}
}
