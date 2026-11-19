$ErrorActionPreference='Stop'
Add-Type -TypeDefinition 'using System.Runtime.InteropServices; public class DPI { [DllImport("user32.dll")] public static extern bool SetProcessDPIAware(); }'
[DPI]::SetProcessDPIAware() | Out-Null
Add-Type -AssemblyName System.Windows.Forms,System.Drawing
$form=New-Object System.Windows.Forms.Form
$form.Text='VNC latency test (temporary)';$form.StartPosition='Manual'
$form.Location=New-Object System.Drawing.Point(2020,80);$form.Size=New-Object System.Drawing.Size(450,240)
$form.TopMost=$true;$form.KeyPreview=$true
$label=New-Object System.Windows.Forms.Label;$label.Location=New-Object System.Drawing.Point(16,12);$label.Size=New-Object System.Drawing.Size(400,40);$label.Text='Input-to-image latency test. F8 or click the square.';$form.Controls.Add($label)
$panel=New-Object System.Windows.Forms.Panel;$panel.Location=New-Object System.Drawing.Point(30,65);$panel.Size=New-Object System.Drawing.Size(180,100);$panel.BackColor=[Drawing.Color]::Black;$form.Controls.Add($panel)
$script:n=0
$toggle={ $script:n++;$panel.BackColor=if($script:n%2){[Drawing.Color]::White}else{[Drawing.Color]::Black};$panel.Refresh();$label.Text="Received events: $script:n";[IO.File]::WriteAllText("$PSScriptRoot\latency-result.txt",[string]$script:n) }
$form.Add_KeyDown({if($_.KeyCode -eq 'F8') {& $toggle;$_.Handled=$true}})
$panel.Add_MouseDown({& $toggle;$form.Activate()})
$form.Add_Shown({$form.Activate();$p=$panel.PointToScreen((New-Object System.Drawing.Point(80,50)));[IO.File]::WriteAllText("$PSScriptRoot\latency-ready.txt", "$($p.X),$($p.Y)")})
$timer=New-Object System.Windows.Forms.Timer;$timer.Interval=600000;$timer.Add_Tick({$form.Close()});$timer.Start()
[Windows.Forms.Application]::Run($form)
