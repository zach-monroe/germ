// keylat: key-to-screen latency of a terminal window on X.
//   keylat WINDOW_ID [N]
// Types 'x' with XTest N times (Backspace in between), each time polling
// the window's contents until they change, and prints the median and
// 95th percentile in milliseconds. Run the terminal with `cat` so the
// kernel echoes the keys.
#include <X11/Xlib.h>
#include <X11/Xutil.h>
#include <X11/extensions/XTest.h>
#include <X11/keysym.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

static double now(void) {
	struct timespec t;
	clock_gettime(CLOCK_MONOTONIC, &t);
	return t.tv_sec * 1e3 + t.tv_nsec / 1e6;
}

static unsigned long sum(Display *d, Window w, int width, int height) {
	XImage *img = XGetImage(d, w, 0, 0, width, height, AllPlanes, ZPixmap);
	if (!img) return 0;
	unsigned long s = 0;
	int n = img->bytes_per_line * img->height;
	for (int i = 0; i < n; i += 4) s = s * 31 + (unsigned char)img->data[i] + ((unsigned char)img->data[i+1] << 8);
	XDestroyImage(img);
	return s;
}

static int cmp(const void *a, const void *b) {
	double x = *(const double *)a, y = *(const double *)b;
	return x < y ? -1 : x > y;
}

static double wait_change(Display *d, Window w, int width, int height, unsigned long before) {
	double t0 = now();
	while (now() - t0 < 1000) {
		if (sum(d, w, width, height) != before) return now() - t0;
	}
	return -1;
}

static void key(Display *d, KeySym ks) {
	KeyCode kc = XKeysymToKeycode(d, ks);
	XTestFakeKeyEvent(d, kc, True, 0);
	XTestFakeKeyEvent(d, kc, False, 0);
	XFlush(d);
}

int main(int argc, char **argv) {
	if (argc < 2) { fprintf(stderr, "usage: keylat WINDOW [N]\n"); return 2; }
	Display *d = XOpenDisplay(NULL);
	if (!d) { fprintf(stderr, "no display\n"); return 1; }
	Window w = strtoul(argv[1], NULL, 0);
	int n = argc > 2 ? atoi(argv[2]) : 50;
	XWindowAttributes at;
	XGetWindowAttributes(d, w, &at);
	// Only the top-left area: where `cat` echoes.
	int width = at.width < 400 ? at.width : 400, height = at.height < 120 ? at.height : 120;
	XSetInputFocus(d, w, RevertToParent, CurrentTime);
	usleep(300000);
	double *lat = calloc(n, sizeof *lat);
	int got = 0;
	for (int i = 0; i < n; i++) {
		unsigned long before = sum(d, w, width, height);
		key(d, XK_x);
		double l = wait_change(d, w, width, height, before);
		if (l >= 0) lat[got++] = l;
		usleep(60000);
		before = sum(d, w, width, height);
		key(d, XK_BackSpace);
		wait_change(d, w, width, height, before);
		usleep(60000 + (i * 7919 % 17) * 1000); // decorrelate from the frame clock
	}
	if (!got) { printf("no changes seen\n"); return 1; }
	qsort(lat, got, sizeof *lat, cmp);
	printf("median %.1f ms  p95 %.1f ms  (n=%d)\n", lat[got / 2], lat[got * 95 / 100], got);
	return 0;
}
