package audio

import "errors"

// Duckers runs several duckers as one.
//
// The sound server and the mixer are separate places audio comes from, and a
// user can have both: PipeWire for a browser playing music, an X Air for a
// monitor mix that never touches the computer. Ducking one is no reason to
// leave the other up.
//
// Every member is always called, and the errors are joined. Stopping at the
// first failure would be a way to leave a channel muted or a sink turned down
// with nothing left to put it back — a partial Restore has to keep going, and
// a partial Duck has to be undone by the same Restore that would have run
// anyway.
type Duckers []Ducker

func (ds Duckers) Duck() error {
	var errs []error
	for _, d := range ds {
		if d == nil {
			continue
		}
		if err := d.Duck(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (ds Duckers) Restore() error {
	var errs []error
	for _, d := range ds {
		if d == nil {
			continue
		}
		if err := d.Restore(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
