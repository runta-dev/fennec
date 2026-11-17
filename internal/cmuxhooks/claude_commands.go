package cmuxhooks

// Exact commands emitted by official cmux 0.65.2.2 inject-settings.
// Shell text is recognized by digest, never executed locally.
var claudeCommands = map[string]string{
	"0ef97928020dc1e16877081149212c183533b225b47166fefdb949da464bf64a": "feed",
	"6041f766b773ed7f98469d88fde99cd6da728027e8df773ed64c3bf5dfc67b86": "push-notification",
	"b7f869910ff5c1fb74a2c6a8a16727c7a67e11467633bbd5a053a25a80014975": "cron-create-guard",
	"11ba7c9de4061f2e3d433b456ab261dddda4dbf86040c4c1c3321f7ee0c750fe": "pre-tool-use",
	"8e7bf955dd719a02e2c84d8135558603bf2275d371c4e9bf878dfbe4f07123b1": "session-end",
	"3f0df0ec05838dd95e5a22414c9c08c5d0191c4ca8689c0e2c02c3cc0a5a2ea0": "feed",
	"72697cb57d154228641a35a3cae1c390909e8cdff06945e1bec2a84f1aa6fd8a": "notification",
	"3619a5a3dc441d722ff529d4c5647e91806f4ab4678290d7fabb8e67f1c38f58": "session-start",
	"4bf38f5d604009a4584b47daac0b49ec25aa28423279fbb77a29297557b540ac": "inbox-wait",
	"45b7b0906d71cebc3b6e5ec1e96760eb24badc692de8e16aa516506ea580d58b": "stop",
	"6542ca2118e8e5c282e8e734a014087b0a6f483b6323fb5ced7be4defaede03d": "auto-name",
	"89c582c25801f3b1d57e6c7db4c09c682f3a44222df5d290183ec709860c1663": "prompt-submit",
	"e4f35a04a89c091b148ac77f102a26f4472e92f638c6c66e65fb96a48a1d7d23": "inbox-drain",
}
