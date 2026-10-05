package device

import "reflect"

// Rebase moves a local edit buffer onto a fresh read of the Device — the
// three-way merge behind "apply the pending changes" (spec user story 19):
// regions the user edited keep their values, every region they did not touch
// follows the Device. An apply of the result writes the user's changes and
// nothing else: a field the user never touched is never written back from a
// stale copy of an older read.
//
// base is the Device state the buffer was edited against, fresh is the new
// read, edited is the buffer. Where the buffer still equals base the user
// changed nothing and fresh wins; where it differs the user's value wins —
// what the Device makes of it is the read-back verification's story.
func Rebase(base, fresh, edited State) State {
	return rebaseValue(reflect.ValueOf(base), reflect.ValueOf(fresh), reflect.ValueOf(edited)).Interface().(State)
}

func rebaseValue(base, fresh, edited reflect.Value) reflect.Value {
	if reflect.DeepEqual(base.Interface(), edited.Interface()) {
		return fresh // untouched here: the Device's value is the truth
	}
	switch edited.Kind() {
	case reflect.Struct:
		out := reflect.New(edited.Type()).Elem()
		for i := 0; i < edited.NumField(); i++ {
			out.Field(i).Set(rebaseValue(base.Field(i), fresh.Field(i), edited.Field(i)))
		}
		return out
	case reflect.Array, reflect.Slice:
		out := reflect.New(edited.Type()).Elem()
		for i := 0; i < edited.Len(); i++ {
			out.Index(i).Set(rebaseValue(base.Index(i), fresh.Index(i), edited.Index(i)))
		}
		return out
	default:
		return edited // an edited value keeps the user's choice
	}
}
