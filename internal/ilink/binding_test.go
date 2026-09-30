package ilink

import "testing"

func TestExplicitSingleBinding(t *testing.T) {
	root := t.TempDir()
	first := &Credentials{ILinkBotID: "first", ILinkUserID: "owner-1"}
	second := &Credentials{ILinkBotID: "second", ILinkUserID: "owner-2"}
	accounts := []*Credentials{first, second}
	if _, err := ActiveBinding(root, accounts); err == nil {
		t.Fatal("ambiguous accounts accepted")
	}
	if active, err := ActiveBinding(root, accounts[:1]); err != nil || active != first {
		t.Fatalf("single binding: %v", err)
	}
	if err := SelectBinding(root, "second", accounts); err != nil {
		t.Fatal(err)
	}
	if active, err := ActiveBinding(root, accounts); err != nil || active != second {
		t.Fatalf("selection: %v", err)
	}
	if _, err := ActiveBinding(root, accounts[:1]); err == nil {
		t.Fatal("missing selected account silently fell back")
	}
}
