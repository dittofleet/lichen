package notify

import (
	"os"
	"os/exec"
)

// toast shows the message in LICHEN_NOTICE. Windows only shows a
// notification from an app it knows, and lichen is not installed as one,
// so it goes up as Windows PowerShell's, which runs it.
const toast = `$null = [Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime]
$xml = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent('ToastText02')
$text = $xml.GetElementsByTagName('text')
$null = $text.Item(0).AppendChild($xml.CreateTextNode('lichen'))
$null = $text.Item(1).AppendChild($xml.CreateTextNode($env:LICHEN_NOTICE))
$app = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe'
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($app).Show([Windows.UI.Notifications.ToastNotification]::new($xml))`

// command is what puts the notification up.
func command(msg string) *exec.Cmd {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", toast)
	cmd.Env = append(os.Environ(), "LICHEN_NOTICE="+msg)
	return cmd
}
