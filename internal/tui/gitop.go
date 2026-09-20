package tui

import (
	"charm.land/bubbles/v2/key"
)

// GitOpKind names an operation of the workstation. The view says which one
// was asked for and what it applies to; what it runs, and whether it asks
// before running, belongs to whoever answers.
type GitOpKind int

const (
	// The working tree.
	OpStage GitOpKind = iota
	OpStageAll
	OpUnstage
	OpUnstageAll
	OpDiscard
	OpDiscardAll
	// The commit in front of you.
	OpCommit
	OpAmend
	OpRewordHead
	// The refs of the project.
	OpCheckout
	OpBranchHere
	OpRename
	OpDelete
	OpSetUpstream
	OpMerge
	OpRebase
	OpStashPop
	OpStashApply
	OpWorktreeAdd
	// One commit of the history.
	OpCherryPick
	OpRevert
	OpResetSoft
	OpResetMixed
	OpResetHard
	OpTag
	OpTagAnnotated
	OpPatch
	OpCopyOID
	// The history rewritten.
	OpReword
	OpDrop
	OpSquash
	OpFixup
	OpMoveUp
	OpMoveDown
	// The remotes.
	OpFetch
	OpPull
	OpPush
	OpPushUpstream
	// What git stopped in the middle of.
	OpContinue
	OpSkip
	OpAbort
	gitOpCount
)

// opNames are what each operation is called, for a message and for the keys
// the workstation lists.
var opNames = [gitOpCount]string{
	OpStage:        "stage the file",
	OpStageAll:     "stage everything",
	OpUnstage:      "unstage the file",
	OpUnstageAll:   "unstage everything",
	OpDiscard:      "discard the file",
	OpDiscardAll:   "discard everything",
	OpCommit:       "commit what is staged",
	OpAmend:        "amend the last commit",
	OpRewordHead:   "edit the message of HEAD",
	OpCheckout:     "check it out",
	OpBranchHere:   "open a branch here",
	OpRename:       "rename it",
	OpDelete:       "get rid of it",
	OpSetUpstream:  "follow it",
	OpMerge:        "merge it in",
	OpRebase:       "rebase onto it",
	OpStashPop:     "pop it",
	OpStashApply:   "apply it",
	OpWorktreeAdd:  "open a worktree",
	OpCherryPick:   "cherry pick it",
	OpRevert:       "revert it",
	OpResetSoft:    "move the branch here",
	OpResetMixed:   "move the branch here, keeping the changes",
	OpResetHard:    "move the branch here, writing the tree over",
	OpTag:          "tag it",
	OpTagAnnotated: "tag it with a message",
	OpPatch:        "write a patch",
	OpCopyOID:      "copy the object name",
	OpReword:       "reword it",
	OpDrop:         "drop it from the history",
	OpSquash:       "fold it into the one before",
	OpFixup:        "fold it into the one before, keeping that message",
	OpMoveUp:       "move it up",
	OpMoveDown:     "move it down",
	OpFetch:        "fetch and prune",
	OpPull:         "pull",
	OpPush:         "push",
	OpPushUpstream: "push and follow",
	OpContinue:     "carry on",
	OpSkip:         "leave this one out",
	OpAbort:        "put the branch back",
}

// String names an operation.
func (k GitOpKind) String() string {
	if k < 0 || k >= gitOpCount {
		return "unknown"
	}
	return opNames[k]
}

// GitOp is what the user asked for: the operation and what it applies to. The
// fields it does not use are empty, so one record carries every operation.
type GitOp struct {
	Kind GitOpKind
	// Rev is the commit or the full name of the ref it applies to, Name what
	// that ref is called, Path the file or the directory of a worktree.
	Rev  string
	Name string
	Path string
	// Index is the stash entry it applies to, -1 when it applies to none.
	Index int
	// Text is what was typed into the form that asked: a message, a branch
	// name, a tag.
	Text string
	// Confirmed says the form that stands before this operation was
	// answered, so the same operation asked for again is the one to run.
	Confirmed bool
}

// GitOpMsg is an operation the workstation asks for. Whoever answers decides
// whether it runs as it stands or whether a form stands before it.
type GitOpMsg struct{ Op GitOp }

// GitAskMsg puts a form up before an operation runs. The workstation asks for
// the operation again, confirmed and carrying what was typed, once the form
// has been answered.
type GitAskMsg struct {
	Form GitForm
	Op   GitOp
}

// gitOpNeed says what an operation applies to, so a key with nothing under it
// does nothing rather than asking for an operation on nothing.
type gitOpNeed int

const (
	// needNothing is an operation about the repository itself, needRef one
	// about the row of the refs pane, needCommit one about the commit the
	// history is on, needFile one about the file the detail is on, and
	// needRunning one about what git stopped in the middle of.
	needNothing gitOpNeed = iota
	needRef
	needCommit
	needFile
	needRunning
)

// anyRegion is a key that works wherever the cursor stands.
const anyRegion GitRegion = -1

// gitOpKey is one key of the workstation and the operation it asks for.
type gitOpKey struct {
	binding key.Binding
	region  GitRegion
	kind    GitOpKind
	need    gitOpNeed
}

// gitOpKeys are every operation on a key. A key means the same thing
// wherever it is pressed: c checks out whatever the cursor is on, d gets rid
// of it, b opens a branch at it. What reaches a remote is a capital, so no
// push is one lower case letter away, and the keys the history is moved with
// are apart from the ones that only read it.
//
// A key may stand for more than one operation: y carries on what git stopped
// in the middle of while there is one, and cherry picks the commit the cursor
// is on when there is not. The first one whose target is there wins, so a key
// never asks for an operation on nothing.
func gitOpKeys() []gitOpKey {
	op := func(name string, region GitRegion, kind GitOpKind, need gitOpNeed) gitOpKey {
		return gitOpKey{
			binding: key.NewBinding(key.WithKeys(name), key.WithHelp(name, kind.String())),
			region:  region, kind: kind, need: need,
		}
	}
	return []gitOpKey{
		// What git stopped in the middle of, wherever the cursor stands.
		op("y", anyRegion, OpContinue, needRunning),
		op("!", anyRegion, OpSkip, needRunning),
		op("Z", anyRegion, OpAbort, needRunning),
		// The remotes.
		op("F", anyRegion, OpFetch, needNothing),
		op("L", anyRegion, OpPull, needNothing),
		op("P", anyRegion, OpPush, needNothing),
		op("O", anyRegion, OpPushUpstream, needNothing),

		// The refs of the project.
		op("c", RegionRefs, OpCheckout, needRef),
		op("b", RegionRefs, OpBranchHere, needRef),
		op("m", RegionRefs, OpMerge, needRef),
		op("B", RegionRefs, OpRebase, needRef),
		op("i", RegionRefs, OpRename, needRef),
		op("d", RegionRefs, OpDelete, needRef),
		op("u", RegionRefs, OpSetUpstream, needRef),
		op("p", RegionRefs, OpStashPop, needRef),
		op("a", RegionRefs, OpStashApply, needRef),
		op("A", RegionRefs, OpWorktreeAdd, needRef),

		// One commit of the history.
		op("c", RegionGraph, OpCheckout, needCommit),
		op("b", RegionGraph, OpBranchHere, needCommit),
		op("A", RegionGraph, OpWorktreeAdd, needCommit),
		op("t", RegionGraph, OpTag, needCommit),
		op("T", RegionGraph, OpTagAnnotated, needCommit),
		op("y", RegionGraph, OpCherryPick, needCommit),
		op("v", RegionGraph, OpRevert, needCommit),
		op("m", RegionGraph, OpResetMixed, needCommit),
		op("M", RegionGraph, OpResetSoft, needCommit),
		op("H", RegionGraph, OpResetHard, needCommit),
		op("e", RegionGraph, OpReword, needCommit),
		op("d", RegionGraph, OpDrop, needCommit),
		op("S", RegionGraph, OpSquash, needCommit),
		op("f", RegionGraph, OpFixup, needCommit),
		op("[", RegionGraph, OpMoveUp, needCommit),
		op("]", RegionGraph, OpMoveDown, needCommit),
		op("p", RegionGraph, OpPatch, needCommit),
		op("o", RegionGraph, OpCopyOID, needCommit),

		// The working tree and the commit in front of you.
		op("s", RegionDetail, OpStage, needFile),
		op("S", RegionDetail, OpStageAll, needNothing),
		op("u", RegionDetail, OpUnstage, needFile),
		op("U", RegionDetail, OpUnstageAll, needNothing),
		op("x", RegionDetail, OpDiscard, needFile),
		op("X", RegionDetail, OpDiscardAll, needNothing),
		op("C", RegionDetail, OpCommit, needNothing),
		op("M", RegionDetail, OpAmend, needNothing),
		op("e", RegionDetail, OpRewordHead, needNothing),
	}
}
