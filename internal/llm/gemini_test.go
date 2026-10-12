package llm

import (
	"reflect"
	"testing"

	"google.golang.org/genai"
)

func TestResponseTextAggregatesVisibleParts(t *testing.T) {
	r := &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: "Hello "}, {Text: "hidden", Thought: true}, {Text: "world"}}}}}}
	if got, want := responseText(r), []string{"Hello ", "world"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
