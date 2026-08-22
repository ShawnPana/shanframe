// Sleep/wake notices from IOKit so the agent can say "asleep" before the
// socket dies (and viewers see "asleep", not "offline"). Runs its own
// CFRunLoop thread; the Go side just gets a callback.
#include <IOKit/pwr_mgt/IOPMLib.h>
#include <IOKit/IOMessage.h>
#include <CoreFoundation/CoreFoundation.h>
#include <pthread.h>

extern void sfPowerEvent(int asleep);
static io_connect_t rootPort;

static void cb(void *refCon, io_service_t service, natural_t msgType, void *msgArg) {
	switch (msgType) {
	case kIOMessageSystemWillSleep:
		sfPowerEvent(1);
		IOAllowPowerChange(rootPort, (long)msgArg);
		break;
	case kIOMessageCanSystemSleep:
		IOAllowPowerChange(rootPort, (long)msgArg);
		break;
	case kIOMessageSystemHasPoweredOn:
		sfPowerEvent(0);
		break;
	}
}

static void *runloop(void *arg) {
	IONotificationPortRef port;
	io_object_t notifier;
	rootPort = IORegisterForSystemPower(NULL, &port, cb, &notifier);
	if (!rootPort) return NULL;
	CFRunLoopAddSource(CFRunLoopGetCurrent(), IONotificationPortGetRunLoopSource(port), kCFRunLoopCommonModes);
	CFRunLoopRun();
	return NULL;
}

int sfPowerWatch(void) {
	pthread_t t;
	if (pthread_create(&t, NULL, runloop, NULL) != 0) return 0;
	pthread_detach(t);
	return 1;
}
