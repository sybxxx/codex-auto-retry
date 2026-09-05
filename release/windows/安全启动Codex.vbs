Option Explicit
Dim shell, fs, base, script, command, code
Set shell = CreateObject("WScript.Shell")
Set fs = CreateObject("Scripting.FileSystemObject")
base = fs.GetParentFolderName(WScript.ScriptFullName)
script = fs.BuildPath(base, "payload\codex-auto-retry\scripts\launch-codex.ps1")
If Not fs.FileExists(script) Then script = fs.BuildPath(fs.GetParentFolderName(fs.GetParentFolderName(base)), "scripts\launch-codex.ps1")
If Not fs.FileExists(script) Then
    MsgBox "Safe launcher is missing. Extract the complete release package first.", 48, "Codex safe launcher"
    WScript.Quit 1
End If
command = Quote(shell.ExpandEnvironmentStrings("%WINDIR%\System32\WindowsPowerShell\v1.0\powershell.exe")) & " -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -File " & Quote(script)
code = shell.Run(command, 0, True)
WScript.Quit code
Function Quote(value)
    Quote = Chr(34) & Replace(value, Chr(34), Chr(34) & Chr(34)) & Chr(34)
End Function
