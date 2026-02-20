package main

import (
	"context"
	"database/sql"
	"testing"

	"github.com/housecat-inc/cheetah"
	"github.com/housecat-inc/cheetah/apps/greet/pkg/db"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGreetings(t *testing.T) {
	tests := []struct {
		_name     string
		greetings []db.CreateGreetingParams
		out       int
	}{
		{
			_name: "empty",
			out:   0,
		},
		{
			_name:     "one greeting",
			greetings: []db.CreateGreetingParams{{Emoji: "👋", Message: "hi", Name: "alice"}},
			out:       1,
		},
		{
			_name: "three greetings",
			greetings: []db.CreateGreetingParams{
				{Emoji: "👋", Message: "hi", Name: "alice"},
				{Emoji: "🎉", Message: "hey", Name: "bob"},
				{Emoji: "🌍", Message: "hello", Name: "charlie"},
			},
			out: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			t.Parallel()

			a := assert.New(t)
			r := require.New(t)

			dbURL := cheetah.TestDB(t)
			conn, err := sql.Open("postgres", dbURL)
			r.NoError(err)
			defer conn.Close()

			q := db.New(conn)
			ctx := context.Background()

			for _, g := range tt.greetings {
				_, err := q.CreateGreeting(ctx, g)
				r.NoError(err)
			}

			greetings, err := q.ListGreetings(ctx)
			a.NoError(err)
			a.Equal(tt.out, len(greetings))
		})
	}
}
