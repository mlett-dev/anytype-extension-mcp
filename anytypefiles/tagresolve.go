package anytypefiles

import (
	"context"
	"fmt"
	"strings"

	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

// TagResolver turns tag names and keys in the property links of the REST
// object tools into option ids. REST takes a tag id or key but no name, while
// objects-modify-property and the query filters take all three; this closes
// the gap. One resolver serves one call, and it loads each property's options
// at most once.
type TagResolver struct {
	prep *filterPrep
}

func (c *Client) NewTagResolver(ctx context.Context, spaceID string) (*TagResolver, error) {
	keys, err := c.loadRelationKeys(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	return &TagResolver{prep: &filterPrep{c: c, ctx: ctx, spaceID: spaceID, keys: keys, options: map[string][]tagOption{}}}, nil
}

// Resolve answers values with every tag name or key replaced by its option id.
// propertyKey is what the property link names: a key in either spelling, or
// the property's id, as REST accepts. What it cannot place comes back as it
// is: a property it does not know or that has no tags, a value no option
// matches, or anything when the options cannot be read. REST refuses whatever
// is no tag of the property, so it keeps the last word and nothing invalid is
// stored. Only a name that several options share is refused here.
func (r *TagResolver) Resolve(propertyKey string, values []string) ([]string, error) {
	key, ok := r.prep.keys.keyByID[propertyKey]
	if !ok {
		var err error
		if key, err = r.prep.keys.resolve(propertyKey); err != nil {
			return values, nil
		}
	}
	if format, known := r.prep.format(key); !known || (format != model.RelationFormat_tag && format != model.RelationFormat_status) {
		return values, nil
	}
	opts, err := r.prep.tagOptions(key)
	if err != nil {
		return values, nil
	}
	out := make([]string, len(values))
	for i, v := range values {
		hits := tagMatches(opts, strings.TrimSpace(v))
		switch len(hits) {
		case 0:
			out[i] = v
		case 1:
			out[i] = hits[0].id
		default:
			ids := make([]string, 0, len(hits))
			for _, h := range hits {
				ids = append(ids, h.id)
			}
			return nil, fmt.Errorf("%s has %d tags named %q; pass the id of the one you mean: %s",
				r.prep.keys.describe(key), len(hits), v, strings.Join(ids, ", "))
		}
	}
	return out, nil
}
