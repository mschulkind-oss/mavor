//go:build ignore

// Native Wayland focus, pointer and explicit Paste fixture. No production typing simulation.
#include <gtk/gtk.h>
#include <stdio.h>
#include <string.h>
static GtkWindow *window;
static GtkTextView *view;
static void received(GObject *object,GAsyncResult *result,gpointer label){
 GError *error=NULL;char *text=gdk_clipboard_read_text_finish(GDK_CLIPBOARD(object),result,&error);
 printf("%s\t%s\t%s\n",(char*)label,text?text:"<null>",error?error->message:"OK");fflush(stdout);g_free(text);g_clear_error(&error);
}
static gboolean command(GIOChannel *channel,GIOCondition condition,gpointer unused){
 (void)unused;if(condition&G_IO_HUP)return G_SOURCE_REMOVE;char *line=NULL;
 if(g_io_channel_read_line(channel,&line,NULL,NULL,NULL)!=G_IO_STATUS_NORMAL)return G_SOURCE_CONTINUE;
 g_strstrip(line);GtkTextIter start,end;GtkTextBuffer *buffer=gtk_text_view_get_buffer(view);GdkDisplay *display=gdk_display_get_default();
 if(!strcmp(line,"select")){
  gtk_widget_grab_focus(GTK_WIDGET(view));gtk_text_buffer_get_bounds(buffer,&start,&end);gtk_text_buffer_select_range(buffer,&start,&end);puts("SELECTED");
 }else if(!strcmp(line,"seed-primary")){
  gdk_clipboard_set_text(gdk_display_get_primary_clipboard(display),"primary coexistence sentinel α");puts("SEEDED");
 }else if(!strcmp(line,"primary") || !strcmp(line,"clipboard")){
  gboolean primary=!strcmp(line,"primary");gdk_clipboard_read_text_async(primary?gdk_display_get_primary_clipboard(display):gdk_display_get_clipboard(display),NULL,received,primary?"primary":"clipboard");
 }else if(!strcmp(line,"paste")){
  // Only this explicit fixture command invokes GTK's actual editable Paste action.
  if(!gtk_widget_activate_action(GTK_WIDGET(view),"clipboard.paste",NULL))puts("PASTE_FAILED");
 }else{
  gtk_text_buffer_get_bounds(buffer,&start,&end);char *text=gtk_text_buffer_get_text(buffer,&start,&end,FALSE);
  printf("STATE\t%s\tEND\n",text);g_free(text);
  if(!strcmp(line,"inspect")){
   gboolean selected=gtk_text_buffer_get_selection_bounds(buffer,&start,&end);
   printf("SELECTION\t%d\t%d\t%d\n",selected,gtk_text_iter_get_offset(&start),gtk_text_iter_get_offset(&end));
  }
 }
 fflush(stdout);g_free(line);return G_SOURCE_CONTINUE;
}
static void edited(GtkTextBuffer *buffer,gpointer unused){
 (void)unused;GtkTextIter start,end;gtk_text_buffer_get_bounds(buffer,&start,&end);char *text=gtk_text_buffer_get_text(buffer,&start,&end,FALSE);printf("ACTION\t%s\n",text);fflush(stdout);g_free(text);
}
static void focus(GObject *o,GParamSpec *s,gpointer u) {
 (void)o;(void)s;(void)u;printf("FOCUS\t%d\n",gtk_window_is_active(window));fflush(stdout);
}
static void click(GtkGestureClick *g,int count,double x,double y,gpointer u) {
 (void)g;(void)count;(void)u;printf("CLICK\t%.0f\t%.0f\n",x,y);fflush(stdout);
}
int main(void){
 gtk_init();window=GTK_WINDOW(gtk_window_new());gtk_window_set_title(window,"Mavor HUD native fixture");gtk_window_set_default_size(window,900,500);
 GtkWidget *box=gtk_box_new(GTK_ORIENTATION_VERTICAL,16);
 GtkWidget *label=gtk_label_new("Session notes — local production HUD proof");gtk_box_append(GTK_BOX(box),label);
 GtkWidget *text=gtk_text_view_new();view=GTK_TEXT_VIEW(text);gtk_text_buffer_set_text(gtk_text_view_get_buffer(GTK_TEXT_VIEW(text)),"Native editor sentinel: preview never emits.",-1);gtk_widget_set_vexpand(text,TRUE);gtk_box_append(GTK_BOX(box),text);gtk_window_set_child(window,box);
 GtkGesture *gesture=gtk_gesture_click_new();gtk_event_controller_set_propagation_phase(GTK_EVENT_CONTROLLER(gesture),GTK_PHASE_CAPTURE);g_signal_connect(gesture,"pressed",G_CALLBACK(click),NULL);gtk_widget_add_controller(GTK_WIDGET(window),GTK_EVENT_CONTROLLER(gesture));
 g_signal_connect(gtk_text_view_get_buffer(view),"changed",G_CALLBACK(edited),NULL);
 g_signal_connect(window,"notify::is-active",G_CALLBACK(focus),NULL);g_io_add_watch(g_io_channel_unix_new(0),G_IO_IN|G_IO_HUP,command,NULL);gtk_window_present(window);g_main_loop_run(g_main_loop_new(NULL,FALSE));
}
