$ErrorActionPreference='Stop'
$base=$PSScriptRoot
# A scheduled-task stop terminates PowerShell without running finally. A private
# Windows Job makes both child processes exit when the launcher handle closes.
Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Diagnostics;
using System.Runtime.InteropServices;
public sealed class SvncTrialJob : IDisposable {
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
    public SvncTrialJob() {
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
$job=New-Object SvncTrialJob
$backend=$null
$agent=$null
try {
  $backend=Start-Process "$base\build\Release\libvnc-desktop.exe" -WorkingDirectory $base -PassThru -WindowStyle Hidden -RedirectStandardOutput "$base\desktop.log" -RedirectStandardError "$base\desktop-error.log"
  $job.Add($backend)
  $agent=Start-Process "$base\..\sshvnc-windows-amd64.exe" -ArgumentList '-config config.yaml' -WorkingDirectory $base -PassThru -WindowStyle Hidden -RedirectStandardOutput "$base\agent-stdout.log" -RedirectStandardError "$base\agent.log"
  $job.Add($agent)
  while(!$agent.WaitForExit(500)) {
    if($backend.HasExited) { throw 'Desktop backend exited' }
  }
  exit $agent.ExitCode
} catch {
  $_ | Out-String | Set-Content "$base\launcher-error.log"
  exit 1
} finally {
  $job.Dispose()
  foreach($child in @($agent,$backend)) {
    if($child -and !$child.HasExited) { Stop-Process -Id $child.Id -ErrorAction SilentlyContinue }
  }
}
