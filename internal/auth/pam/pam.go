package pam

/*
#cgo linux LDFLAGS: -l:libpam.so.0
#include <stdlib.h>
#include <string.h>

typedef struct pam_handle pam_handle_t;

struct pam_message {
	int msg_style;
	const char *msg;
};

struct pam_response {
	char *resp;
	int resp_retcode;
};

struct pam_conv {
	int (*conv)(int, const struct pam_message **, struct pam_response **, void *);
	void *appdata_ptr;
};

extern int pam_start(const char *, const char *, const struct pam_conv *, pam_handle_t **);
extern int pam_end(pam_handle_t *, int);
extern int pam_authenticate(pam_handle_t *, int);
extern int pam_acct_mgmt(pam_handle_t *, int);
extern const char *pam_strerror(pam_handle_t *, int);

struct pilothouse_credentials {
	const char *password;
	const char *username;
};

static void pilothouse_free_responses(struct pam_response *responses, int count) {
	if (responses == NULL) return;
	for (int i = 0; i < count; i++) {
		if (responses[i].resp != NULL) {
			size_t length = strlen(responses[i].resp);
			memset(responses[i].resp, 0, length);
			free(responses[i].resp);
		}
	}
	free(responses);
}

static int pilothouse_conversation(int count, const struct pam_message **messages, struct pam_response **out, void *data) {
	if (count <= 0 || messages == NULL || out == NULL || data == NULL) return 19;
	struct pilothouse_credentials *credentials = data;
	struct pam_response *responses = calloc((size_t)count, sizeof(struct pam_response));
	if (responses == NULL) return 5;
	for (int i = 0; i < count; i++) {
		switch (messages[i]->msg_style) {
		case 1:
			responses[i].resp = strdup(credentials->password);
			break;
		case 2:
			responses[i].resp = strdup(credentials->username);
			break;
		case 3:
		case 4:
			responses[i].resp = NULL;
			break;
		default:
			pilothouse_free_responses(responses, count);
			return 19;
		}
		if ((messages[i]->msg_style == 1 || messages[i]->msg_style == 2) && responses[i].resp == NULL) {
			pilothouse_free_responses(responses, count);
			return 5;
		}
	}
	*out = responses;
	return 0;
}

static int pilothouse_pam_authenticate(const char *service, const char *username, const char *password, char **message) {
	struct pilothouse_credentials credentials = {password, username};
	struct pam_conv conversation = {pilothouse_conversation, &credentials};
	pam_handle_t *handle = NULL;
	int result = pam_start(service, username, &conversation, &handle);
	if (result == 0) result = pam_authenticate(handle, 0);
	if (result == 0) result = pam_acct_mgmt(handle, 0);
	if (result != 0 && message != NULL) {
		const char *description = pam_strerror(handle, result);
		*message = description == NULL ? strdup("authentication failed") : strdup(description);
	}
	if (handle != NULL) pam_end(handle, result);
	return result;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

type Authenticator struct {
	service string
}

func NewAuthenticator(service string) *Authenticator {
	if service == "" {
		service = "pilothouse"
	}
	return &Authenticator{service: service}
}

func (a *Authenticator) Authenticate(username, password string) error {
	service := C.CString(a.service)
	user := C.CString(username)
	secret := C.CString(password)
	defer func() {
		clearCString(secret, len(password))
		C.free(unsafe.Pointer(secret))
		C.free(unsafe.Pointer(user))
		C.free(unsafe.Pointer(service))
	}()
	var message *C.char
	result := C.pilothouse_pam_authenticate(service, user, secret, &message)
	if message != nil {
		defer C.free(unsafe.Pointer(message))
	}
	if result != 0 {
		return fmt.Errorf("PAM authentication failed")
	}
	return nil
}

func clearCString(value *C.char, length int) {
	if value == nil || length <= 0 {
		return
	}
	buffer := unsafe.Slice((*byte)(unsafe.Pointer(value)), length)
	clear(buffer)
}
