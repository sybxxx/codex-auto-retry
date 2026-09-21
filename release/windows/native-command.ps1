# Use .NET pipes rather than PowerShell's native stderr-to-ErrorRecord adapter.
# Both streams are drained concurrently with bounded capture and a deadline.
function Initialize-ReleaseCommandRunner {
    if ('CodexAutoRetry.ReleaseCommand' -as [type]) { return }
    Add-Type -TypeDefinition @'
using System;
using System.Diagnostics;
using System.IO;
using System.Text;
using System.Threading.Tasks;
namespace CodexAutoRetry {
    public sealed class CommandResult {
        public int ExitCode = -1;
        public string Output = "";
        public string ErrorOutput = "";
        public string Failure = "";
    }
    public static class ReleaseCommand {
        private sealed class Capture { public string Text; public bool Truncated; }
        private static async Task<Capture> Read(StreamReader reader, int limit) {
            var buffer = new char[4096];
            var text = new StringBuilder();
            bool truncated = false;
            int n;
            while ((n = await reader.ReadAsync(buffer, 0, buffer.Length).ConfigureAwait(false)) > 0) {
                int keep = Math.Min(n, limit - text.Length);
                text.Append(buffer, 0, keep);
                truncated |= keep != n;
            }
            return new Capture { Text = text.ToString(), Truncated = truncated };
        }
        private static string Quote(string value) {
            var text = new StringBuilder("\"");
            int slashes = 0;
            foreach (char ch in value) {
                if (ch == '\\') { slashes++; continue; }
                if (ch == '"') { text.Append('\\', slashes * 2 + 1); text.Append(ch); }
                else { text.Append('\\', slashes); text.Append(ch); }
                slashes = 0;
            }
            text.Append('\\', slashes * 2); text.Append('"');
            return text.ToString();
        }
        public static CommandResult Run(string path, string[] args, int timeout) {
            var result = new CommandResult();
            using (var process = new Process()) {
                bool started = false;
                process.StartInfo.FileName = path;
                process.StartInfo.Arguments = String.Join(" ", Array.ConvertAll(args, Quote));
                process.StartInfo.UseShellExecute = false;
                process.StartInfo.CreateNoWindow = true;
                process.StartInfo.RedirectStandardOutput = true;
                process.StartInfo.RedirectStandardError = true;
                process.StartInfo.StandardOutputEncoding = new UTF8Encoding(false);
                process.StartInfo.StandardErrorEncoding = new UTF8Encoding(false);
                try {
                    process.Start();
                    started = true;
                    var stdout = Read(process.StandardOutput, 4 * 1024 * 1024);
                    var stderr = Read(process.StandardError, 32768);
                    if (!process.WaitForExit(timeout)) {
                        result.Failure = "timeout";
                        // This handle belongs only to the diagnostic/install child.
                        process.Kill();
                        process.WaitForExit(5000);
                    }
                    if (process.HasExited) result.ExitCode = process.ExitCode;
                    if (!Task.WaitAll(new Task[] { stdout, stderr }, 5000)) {
                        result.Failure = "output_timeout";
                    } else {
                        result.Output = stdout.Result.Text;
                        result.ErrorOutput = stderr.Result.Text;
                        if (stdout.Result.Truncated) result.Failure = "output_limit";
                    }
                } catch {
                    if (result.Failure == "") result.Failure = "process_io";
                } finally {
                    // Do not leave our child running after a pipe/setup failure.
                    // Never enumerate or signal any caller/host process here.
                    if (started) {
                        try {
                            if (!process.HasExited) {
                                process.Kill();
                                if (!process.WaitForExit(5000)) result.Failure = "cleanup_failed";
                            }
                        } catch { result.Failure = "cleanup_failed"; }
                    }
                }
                if (result.Failure != "") result.ExitCode = -1;
            }
            return result;
        }
    }
}
'@
}

function Get-ReleaseCommandFailure {
    param($Result)
    if ($Result.Failure) { return [string]$Result.Failure }
    # Never print raw stderr: it may contain environment values or tokens.
    if ($Result.ErrorOutput -match '(?i)unexpected argument|unrecognized (option|command)|unknown (option|command)') { return 'unsupported_cli_option' }
    if ($Result.ErrorOutput -match '(?i)timed out|timeout|connection|dns|network') { return 'connection_failure' }
    if ($Result.ErrorOutput -match '(?i)permission denied|access.*denied') { return 'access_denied' }
    if ($Result.ErrorOutput -match '(?i)config|toml|manifest|json') { return 'configuration_error' }
    if ($Result.ExitCode -ne 0) { return 'cli_exit_failure' }
    return 'none'
}
