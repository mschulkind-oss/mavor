// Native GTK Wayland selection consumer. Commands request the same clipboard
// transfer an application's Paste action uses; no Shell clipboard APIs.
#include <gtk/gtk.h>
#include <stdio.h>
#include <string.h>
static GtkWindow *window;
static void received(GObject *object, GAsyncResult *result, gpointer label) {
    GError *error = NULL;
    char *text = gdk_clipboard_read_text_finish(GDK_CLIPBOARD(object), result, &error);
    printf("%s\t%s\t%s\n", (char *)label, text ? text : "<null>", error ? error->message : "OK");
    fflush(stdout);
    g_free(text);
    g_clear_error(&error);
    g_free(label);
}
static gboolean command(GIOChannel *channel, GIOCondition condition, gpointer unused) {
    (void)unused;
    if (condition & G_IO_HUP) return G_SOURCE_REMOVE;
    char *line = NULL;
    if (g_io_channel_read_line(channel, &line, NULL, NULL, NULL) != G_IO_STATUS_NORMAL) return G_SOURCE_CONTINUE;
    g_strstrip(line);
    GdkDisplay *display = gdk_display_get_default();
    if (!strcmp(line, "seed-primary")) {
        gdk_clipboard_set_text(gdk_display_get_primary_clipboard(display), "primary sentinel α");
        puts("SEEDED"); fflush(stdout);
    } else {
        gboolean primary = !strcmp(line, "primary");
        gdk_clipboard_read_text_async(primary ? gdk_display_get_primary_clipboard(display) : gdk_display_get_clipboard(display), NULL, received, g_strdup(line));
    }
    g_free(line);
    return G_SOURCE_CONTINUE;
}
static void focus(GObject *object, GParamSpec *spec, gpointer unused) {
    (void)object; (void)spec; (void)unused;
    printf("FOCUS\t%d\n", gtk_window_is_active(window)); fflush(stdout);
}
int main(int argc, char **argv) {
    gtk_init();
    window = GTK_WINDOW(gtk_window_new());
    gtk_window_set_title(window, argc > 1 ? argv[1] : "Mavor native clipboard consumer");
    gtk_window_set_default_size(window, 600, 300);
    gtk_window_set_child(window, gtk_label_new("Native manual-paste selection consumer"));
    g_signal_connect(window, "notify::is-active", G_CALLBACK(focus), NULL);
    g_io_add_watch(g_io_channel_unix_new(0), G_IO_IN | G_IO_HUP, command, NULL);
    gtk_window_present(window);
    g_main_loop_run(g_main_loop_new(NULL, FALSE));
}
