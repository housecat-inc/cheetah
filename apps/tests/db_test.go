package main

import (
	"context"
	"database/sql"
	"testing"

	"github.com/housecat-inc/cheetah"
	"github.com/housecat-inc/cheetah/apps/tests/pkg/db"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestItems(t *testing.T) {
	tests := []struct {
		_name string
		items []db.CreateItemParams
		out   int
	}{
		{
			_name: "empty",
			out:   0,
		},
		{
			_name: "one item",
			items: []db.CreateItemParams{{Name: "a", Value: "1"}},
			out:   1,
		},
		{
			_name: "three items",
			items: []db.CreateItemParams{
				{Name: "a", Value: "1"},
				{Name: "b", Value: "2"},
				{Name: "c", Value: "3"},
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

			for _, item := range tt.items {
				_, err := q.CreateItem(ctx, item)
				r.NoError(err)
			}

			items, err := q.ListItems(ctx)
			a.NoError(err)
			a.Equal(tt.out, len(items))
		})
	}
}
