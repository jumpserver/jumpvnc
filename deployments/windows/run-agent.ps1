param(
  [Parameter(Mandatory=$true)][string]$Agent,
  [Parameter(Mandatory=$true)][string]$Config
)
$ErrorActionPreference='Stop'
$base=Join-Path (Split-Path -Parent $Config) ([IO.Path]::GetFileNameWithoutExtension($Config))
# A scheduled-task stop terminates PowerShell without running finally. A private
# Windows Job makes both child processes exit when the launcher handle closes.
Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Diagnostics;
using System.Runtime.InteropServices;
public sealed class SvncAgentJob : IDisposable {
    [StructLayout(LayoutKind.Sequential)] struct Basic {
        public long ProcessTime, JobTime; public uint Flags;
        public UIntPtr MinWorkingSet, MaxWorkingSet; public uint ActiveProcesses;
        public UIntPtr Affinity; public uint Priority, Scheduling;
    }
    [StructLayout(LayoutKind.Sequential)] struct Io {
        public ulong ReadOps, WriteOps, OtherOps, ReadBytes, WriteBytes, OtherBytes;
    }
    [StructLayout(LayoutKind.Sequential)] struct Limits {
        public Basic Basic; public Io Io;
        public UIntPtr ProcessMemory, JobMemory, PeakProcessMemory, PeakJobMemory;
    }
    [DllImport("kernel32.dll", SetLastError=true)] static extern IntPtr CreateJobObject(IntPtr attributes, string name);
    [DllImport("kernel32.dll", SetLastError=true)] static extern bool SetInformationJobObject(IntPtr job, int type, ref Limits limits, uint size);
    [DllImport("kernel32.dll", SetLastError=true)] static extern bool AssignProcessToJobObject(IntPtr job, IntPtr process);
    [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr handle);
    IntPtr handle;
    public SvncAgentJob() {
        handle=CreateJobObject(IntPtr.Zero,null);
        if(handle==IntPtr.Zero) throw new Win32Exception();
        var limits=new Limits(); limits.Basic.Flags=0x2000; // KILL_ON_JOB_CLOSE
        if(!SetInformationJobObject(handle,9,ref limits,(uint)Marshal.SizeOf(typeof(Limits)))) {
            int error=Marshal.GetLastWin32Error(); Dispose(); throw new Win32Exception(error);
        }
    }
    public void Add(Process process) {
        if(!AssignProcessToJobObject(handle,process.Handle)) throw new Win32Exception();
    }
    public void Dispose() { if(handle!=IntPtr.Zero) { CloseHandle(handle); handle=IntPtr.Zero; } }
}
'@
$job=New-Object SvncAgentJob
$childProcess=$null
try {
  $childProcess=Start-Process -FilePath $Agent -ArgumentList ('-config "'+$Config+'"') -WorkingDirectory (Split-Path -Parent $Config) -PassThru -WindowStyle Hidden -RedirectStandardOutput "$base.stdout.log" -RedirectStandardError "$base.agent.log"
  $job.Add($childProcess)
  Remove-Item "$base.launcher-error.log" -ErrorAction SilentlyContinue
  $childProcess.WaitForExit()
  exit $childProcess.ExitCode
} catch {
  $_ | Out-String | Set-Content "$base.launcher-error.log"
  exit 1
} finally {
  $job.Dispose()
  foreach($child in @($childProcess)) {
    if($child -and !$child.HasExited) { Stop-Process -Id $child.Id -ErrorAction SilentlyContinue }
  }
}
