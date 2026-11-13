package chromeimport

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <stdlib.h>
#include <string.h>
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>

// readGenericPassword reads a generic password item; macOS asks the user
// whether this program may use it. The caller frees *out with free().
static OSStatus readGenericPassword(const char *service, const char *account, void **out, size_t *len) {
	CFStringRef svc = CFStringCreateWithCString(NULL, service, kCFStringEncodingUTF8);
	CFStringRef acct = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);
	const void *keys[] = { kSecClass, kSecAttrService, kSecAttrAccount, kSecReturnData, kSecMatchLimit };
	const void *vals[] = { kSecClassGenericPassword, svc, acct, kCFBooleanTrue, kSecMatchLimitOne };
	CFDictionaryRef q = CFDictionaryCreate(NULL, keys, vals, 5, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFTypeRef result = NULL;
	OSStatus st = SecItemCopyMatching(q, &result);
	CFRelease(q);
	CFRelease(svc);
	CFRelease(acct);
	*out = NULL;
	*len = 0;
	if (st == errSecSuccess && result != NULL) {
		CFDataRef d = (CFDataRef)result;
		*len = (size_t)CFDataGetLength(d);
		*out = malloc(*len ? *len : 1);
		if (*out) memcpy(*out, CFDataGetBytePtr(d), *len);
		CFRelease(result);
	}
	return st;
}
*/
import "C"

import (
	"context"
	"fmt"
	"unsafe"
)

// Keychain reads a Chromium-family Safe Storage password from the login
// Keychain. The access prompt macOS shows is the user's consent; it is
// never bypassed, and the password is not cached.
type Keychain struct {
	Service string // "Chrome Safe Storage"
	Account string // "Chrome"
}

// ChromeKeychain is Google Chrome's item.
var ChromeKeychain = Keychain{Service: "Chrome Safe Storage", Account: "Chrome"}

// SafeStoragePassword reads the item (the prompt may wait for the user).
func (k Keychain) SafeStoragePassword(ctx context.Context) ([]byte, error) {
	type res struct {
		pw  []byte
		err error
	}
	done := make(chan res, 1)
	go func() {
		svc := C.CString(k.Service)
		acct := C.CString(k.Account)
		defer C.free(unsafe.Pointer(svc))
		defer C.free(unsafe.Pointer(acct))
		var out unsafe.Pointer
		var n C.size_t
		st := C.readGenericPassword(svc, acct, &out, &n)
		if out != nil {
			defer C.free(out)
		}
		switch st {
		case C.errSecSuccess:
			b := C.GoBytes(out, C.int(n))
			// Scrub the C copy before freeing it.
			C.memset(out, 0, n)
			done <- res{pw: b}
		case C.errSecItemNotFound:
			done <- res{err: fmt.Errorf("the Keychain has no %q item; is Google Chrome set up on this Mac?", k.Service)}
		case C.errSecUserCanceled, C.errSecAuthFailed, C.errSecInteractionNotAllowed:
			done <- res{err: ErrKeyDenied}
		default:
			done <- res{err: fmt.Errorf("the Keychain refused access to %q (OSStatus %d)", k.Service, int(st))}
		}
	}()
	select {
	case r := <-done:
		return r.pw, r.err
	case <-ctx.Done():
		// The prompt may still be answered; whatever arrives is scrubbed.
		go func() { r := <-done; clear(r.pw) }()
		return nil, ctx.Err()
	}
}
