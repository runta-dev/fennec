package flows

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strconv"

	"fennec/cua/internal/models"
	"fennec/cua/internal/protocol"
)

// A model pick names a flow only at this probability or above, and only
// this far ahead of the runner-up.
const (
	PickMin    = 0.6
	PickMargin = 0.25
)

// Decider asks the decide model (models.Client in production).
type Decider interface {
	Decide(ctx context.Context, args models.DecideArgs) (*models.Decided, error)
}

// Found is a find's answer: the flow the model picked, or none, with the
// probability of its top choice.
type Found struct {
	Flow   *Saved
	P      float64
	Served *protocol.Served
}

// Find asks the model which indexed flow does what query asks, or none.
func (s *Store) Find(ctx context.Context, query string, d Decider) (*Found, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	lines, all, err := s.indexLines()
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return &Found{}, nil
	}
	if d == nil {
		return nil, &protocol.Error{Code: protocol.ErrModelUnavailable, Message: "finding a flow by intent needs the decide model, and none is configured"}
	}
	criteria := map[string]string{"none": "none of these flows does it"}
	for i, l := range lines {
		criteria["f"+strconv.Itoa(i)] = l
	}
	questions, _ := json.Marshal(map[string]any{"pick": map[string]any{
		"type":         models.Choice,
		"instructions": "Which saved desktop flow does this request ask for?",
		"criteria":     criteria,
	}})
	state, _ := json.Marshal(map[string]string{"request": query})
	got, err := d.Decide(ctx, models.DecideArgs{State: state, Questions: questions})
	if err != nil {
		return nil, err
	}
	ans := got.Answers["pick"]
	probs := make([]float64, 0, len(ans.Probabilities))
	for _, p := range ans.Probabilities {
		probs = append(probs, p)
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(probs)))
	p := ans.Probabilities[ans.Choice]
	next := 0.0
	if len(probs) > 1 {
		next = probs[1]
	}
	served := got.Served
	out := &Found{P: math.Round(p*100) / 100, Served: &served}
	if ans.Choice == "none" || p < PickMin || p-next < PickMargin {
		return out, nil
	}
	i, err := strconv.Atoi(ans.Choice[1:])
	if err != nil || ans.Choice[0] != 'f' || i < 0 || i >= len(all) {
		return out, nil
	}
	out.Flow = all[i]
	return out, nil
}
