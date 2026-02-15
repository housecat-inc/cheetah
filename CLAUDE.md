# Standards

## Codebase

Don't add comments, write self-commenting code.
Sort fields lexigrapically when possible (structs, db columns, etc.).
Use `In` and `Out` structs.

## Dependencies

Use `github.com/cockroachdb/errors` and return wrapped errors: `return out, errors.Wrap(err, "foo")`, `return errors.Wrap(foo(), "foo")`
Use `github.com/stretchr/testify/assert`: `a := assert.New()`, `r := require.New()`

## Config

## Backing services

## Build, release, run

## Testing

Write table driven tests with `_name` test cases that assert `a.Equal(tt.out, out)`:

```go
	tests := []struct {
		_name string
		...
		out   foo.Out
	}{}
	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)
			r := require.New(t)
			...
			a.NoError(err)
			a.Equal(tt.out, out)
		}
	}
```

Write end to end-to-end browser tests in `pkg/browser`  with `github.com/go-rod/rod` Chrome DevTools Protocol driver. Build components so browser testing uses simple accessability selectors for clicks, inputs, and matchers. Save before and after screenshots in `T.ArtifactDir`. Assert the Javascript console shows no error messages.
