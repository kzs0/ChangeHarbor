package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Every command the binary advertises must be documented on the CLI page.
func TestDocsCLIReferenceCoversEveryCommand(t *testing.T) {
	b, e := os.ReadFile("../../website/docs/reference/cli.md")
	if e != nil {
		t.Fatal(e)
	}
	page := string(b)
	_, _, usage := runCLI(t)
	list := strings.Fields(usage)[2]
	commands := strings.Split(list, "|")
	_, e = configCommand(context.Background(), nil)
	for _, sub := range strings.Split(strings.Fields(e.Error())[3], "|") {
		commands = append(commands, "config "+sub)
	}
	if len(commands) < 15 {
		t.Fatalf("could not read command list from usage %q", usage)
	}
	for _, c := range commands {
		if !strings.Contains(page, "`reviewd "+c) {
			t.Errorf("cli.md does not document `reviewd %s`", c)
		}
	}
}
