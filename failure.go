package main

import (
	"fmt"
	"os"
)

// failWithGuidance ends a blocking run.
//
// Every rule here is a heuristic over prose, so some fraction of any run is
// wrong — that is a permanent property, not a bug awaiting a fix. A blocking
// linter with an easy suppression therefore has a specific failure mode: the
// cheapest way to green is to silence the finding, and a reviewer skimming a
// diff cannot tell a considered suppression from a reflex one.
//
// The message exists to make the reflex feel wrong. It leads with reading the
// finding, presents suppression as the second option rather than the fix, and
// asks for a reason that says why the finding is WRONG — because "suppressed
// to unblock CI" is exactly the reason that should not survive review.
func failWithGuidance(rule string) {
	fmt.Fprint(os.Stderr, guidance(rule))
	os.Exit(1)
}

// guidance is the text failWithGuidance prints. Split out so it can be tested
// without exiting the process.
func guidance(rule string) string {
	return fmt.Sprintf(`
This check is blocking, and suppressing it is allowed. Please read the findings
before you do.

These rules are heuristics over prose. Some of what they flag is wrong, and
suppressing those is the right call — that is why the escape hatch exists. But
suppression is the SECOND option, not the fix:

  1. Read the finding. Is the comment actually wrong, stale, or duplicated?
     If so, fix the comment. That is the outcome this check is for.

  2. Only if the finding is mistaken, suppress it — and say why it is
     mistaken:

       func f(p string) {} //commentlint:ignore %s  <why this finding is wrong>

     For prose that recurs across many sites, use .commentlint.yml
     (%s under `+"`ignore:`"+`, or an entry under `+"`allow-phrases:`"+`).

A reason is required, and "suppressed to unblock CI" is not one. The reason is
read by the next person who hits the same finding — it should tell them
something they could not work out themselves.

`, rule, rule)
}
