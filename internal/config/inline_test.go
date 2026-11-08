package config

import (
	"strings"
	"testing"
)

func TestInlineCommentsDefaultToShadow(t *testing.T) {
	s, src, e := decode(t, minimal)
	if e != nil {
		t.Fatal(e)
	}
	if s.Repositories["app"].Publish.InlineComments != "shadow" || src["repositories.app.publish.inline_comments"] != "default" {
		t.Fatalf("%+v %v", s.Repositories["app"].Publish, src)
	}
	s, src, e = decode(t, strings.Replace(minimal, `"response":"/r.json"`, `"response":"/r.json","publish":{"inline_comments":"post"}`, 1))
	if e != nil || s.Repositories["app"].Publish.InlineComments != "post" || src["repositories.app.publish.inline_comments"] != "file" {
		t.Fatalf("%v %v", src, e)
	}
}

func TestInlineCommentsEnum(t *testing.T) {
	_, _, e := decode(t, strings.Replace(minimal, `"response":"/r.json"`, `"response":"/r.json","publish":{"inline_comments":"yes"}`, 1))
	if e == nil || !strings.Contains(e.Error(), `repositories.app.publish.inline_comments: "yes" is not one of off, shadow, post`) {
		t.Fatalf("%v", e)
	}
}
