// Package procgroup starts a child process so that a timeout can take down
// everything it spawned, and not just the process that was started.
//
// It exists because two places need it and getting it wrong is invisible until
// something hangs: a deploy step that starts a container build, and a credential
// command that shells out to gpg. Killing only the direct child leaves the
// grandchildren running, holding the terminal, the port or the lock.
package procgroup
