package requests

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

type publicFormBenchmarkCase struct {
	name        string
	occurrences []requestBodyFormOccurrence
	budget      int64
	tooLarge    bool
}

type publicBytesBenchmarkCase struct {
	name     string
	body     []byte
	budget   int64
	tooLarge bool
}

var (
	preparationBenchmarkBodySink PreparedBody
	preparationBenchmarkSink     *RequestPreparation
)

func BenchmarkProjectPreparedPublicForm(b *testing.B) {
	for _, test := range publicFormBenchmarkCases() {
		test := test
		b.Run(test.name, func(b *testing.B) {
			body := requestBodyPlan{
				kind:            requestBodyForm,
				formOccurrences: test.occurrences,
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				prepared, err := projectPreparedBody(context.Background(), body, test.budget)
				if test.tooLarge {
					if !errors.Is(err, ErrPreparationBodyTooLarge) {
						b.Fatalf("projectPreparedBody() error = %v, want ErrPreparationBodyTooLarge", err)
					}
				} else if err != nil {
					b.Fatalf("projectPreparedBody() error = %v", err)
				}
				preparationBenchmarkBodySink = prepared
			}
		})
	}
}

func BenchmarkPreparePublicForm(b *testing.B) {
	client, err := New()
	if err != nil {
		b.Fatal(err)
	}
	for _, test := range publicFormBenchmarkCases() {
		test := test
		builder := benchmarkPublicFormBuilder(client, test.occurrences)
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				preparation, err := builder.Prepare(context.Background(), PrepareOptions{MaxPreparedBodyBytes: test.budget})
				if test.tooLarge {
					if !errors.Is(err, ErrPreparationBodyTooLarge) {
						b.Fatalf("Prepare() error = %v, want ErrPreparationBodyTooLarge", err)
					}
				} else if err != nil {
					b.Fatalf("Prepare() error = %v", err)
				}
				preparationBenchmarkSink = preparation
			}
		})
	}
}

func BenchmarkPreparePublicBytes(b *testing.B) {
	client, err := New()
	if err != nil {
		b.Fatal(err)
	}
	for _, test := range publicBytesBenchmarkCases() {
		test := test
		b.Run(test.name, func(b *testing.B) {
			payload := PublicPayload(test.body)
			builder := client.Post("https://example.test").BytesPayload(payload)
			options := PrepareOptions{MaxPreparedBodyBytes: test.budget}
			ctx := context.Background()

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				preparation, err := builder.Prepare(ctx, options)
				if test.tooLarge {
					if !errors.Is(err, ErrPreparationBodyTooLarge) {
						b.Fatalf("Prepare() error = %v, want ErrPreparationBodyTooLarge", err)
					}
					if preparation != nil {
						b.Fatal("Prepare() returned a preparation with an oversized body")
					}
					continue
				}
				if err != nil {
					b.Fatalf("Prepare() error = %v", err)
				}
				if preparation == nil {
					b.Fatal("Prepare() returned a nil preparation")
				}
				if preparation.body.data.State() != PreparedValuePresent || !bytes.Equal(preparation.body.data.value, test.body) {
					b.Fatalf("Prepare() body = %q, want %q", preparation.body.data.value, test.body)
				}
				preparationBenchmarkSink = preparation
			}
		})
	}
}

func publicFormBenchmarkCases() []publicFormBenchmarkCase {
	large1MiB := strings.Repeat("x", 1<<20)
	large8MiB := strings.Repeat("x", 8<<20)
	invalidUTF8 := string([]byte{0xff, 0xfe})

	cases := []publicFormBenchmarkCase{
		{
			name: "over_budget_ascii_1MiB",
			occurrences: []requestBodyFormOccurrence{
				{name: "payload", value: Public(large1MiB)},
			},
			budget:   1 << 10,
			tooLarge: true,
		},
		{
			name: "over_budget_ascii_8MiB",
			occurrences: []requestBodyFormOccurrence{
				{name: "payload", value: Public(large8MiB)},
			},
			budget:   1 << 10,
			tooLarge: true,
		},
		{
			name: "within_budget_tiny",
			occurrences: []requestBodyFormOccurrence{
				{name: "b", value: Public("two")},
				{name: "a", value: Public("one")},
				{name: "a", value: Public("three")},
			},
		},
		{
			name: "within_budget_mixed",
			occurrences: []requestBodyFormOccurrence{
				{name: "z", value: Public("space value")},
				{name: "a", value: Public("+ % & /\x00")},
				{name: "a", value: Public("")},
				{name: "unicode", value: Public("\u96ea" + invalidUTF8)},
			},
		},
	}
	for i := range cases {
		if cases[i].budget == 0 && !cases[i].tooLarge {
			cases[i].budget = int64(len(benchmarkEncodedForm(cases[i].occurrences)))
		}
	}
	return cases
}

func publicBytesBenchmarkCases() []publicBytesBenchmarkCase {
	return []publicBytesBenchmarkCase{
		{
			name:     "over_budget_1MiB",
			body:     bytes.Repeat([]byte("x"), 1<<20),
			budget:   1 << 10,
			tooLarge: true,
		},
		{
			name:     "over_budget_8MiB",
			body:     bytes.Repeat([]byte("x"), 8<<20),
			budget:   1 << 10,
			tooLarge: true,
		},
		{
			name:   "within_budget_small",
			body:   []byte("public bytes"),
			budget: int64(len("public bytes")),
		},
	}
}

func benchmarkEncodedForm(occurrences []requestBodyFormOccurrence) string {
	values := make(url.Values, len(occurrences))
	for _, occurrence := range occurrences {
		values.Add(occurrence.name, occurrence.value.rawValue())
	}
	return values.Encode()
}

func benchmarkPublicFormBuilder(client *Client, occurrences []requestBodyFormOccurrence) *RequestBuilder {
	builder := client.Post("https://example.test")
	for _, occurrence := range occurrences {
		builder.FormFieldValue(occurrence.name, occurrence.value)
	}
	return builder
}
