param([Parameter(Mandatory=$true)][int]$OwnedProcessId, [Parameter(Mandatory=$true)][string]$ExpectedTitle, [string]$FixturePath, [switch]$Cancel)
$ErrorActionPreference='Stop'
[Console]::OutputEncoding=[Text.Encoding]::UTF8
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
# This driver touches only the native common dialog belonging to the owned
# test process. Water's application UI is operated separately through ctl.
Add-Type -TypeDefinition @"
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;
public static class WaterOwnedDialog {
 public delegate bool EnumProc(IntPtr window,IntPtr data);
 [DllImport("user32.dll")] public static extern bool EnumChildWindows(IntPtr parent,EnumProc callback,IntPtr data);
 [DllImport("user32.dll")] public static extern int GetDlgCtrlID(IntPtr window);
 [DllImport("user32.dll")] public static extern IntPtr GetParent(IntPtr window);
 [DllImport("user32.dll",CharSet=CharSet.Unicode)] public static extern int GetClassName(IntPtr window,StringBuilder text,int count);
 [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr window,out uint pid);
 [DllImport("user32.dll",CharSet=CharSet.Unicode,SetLastError=true)] public static extern IntPtr SendMessageTimeout(IntPtr window,uint message,IntPtr wparam,string text,uint flags,uint timeout,out IntPtr result);
 public static IntPtr FileName(IntPtr dialog,int pid) {
  IntPtr found=IntPtr.Zero;
  EnumChildWindows(dialog,delegate(IntPtr window,IntPtr data){
   uint owner;GetWindowThreadProcessId(window,out owner);if(owner!=pid)return true;
   StringBuilder name=new StringBuilder(128);GetClassName(window,name,128);
   if(name.ToString()!="Edit")return true;
   int id=GetDlgCtrlID(window);
   if(id==1152||id==1148){found=window;return true;}
   for(IntPtr parent=GetParent(window);parent!=IntPtr.Zero&&parent!=dialog;parent=GetParent(parent)) {
    if(GetDlgCtrlID(parent)==1148){found=window;break;}
   }
   return true;
  },IntPtr.Zero);
  return found;
 }
 [DllImport("user32.dll",SetLastError=true)] public static extern bool PostMessage(IntPtr window,uint message,IntPtr wparam,IntPtr lparam);
 public static IntPtr Button(IntPtr dialog,int id,int pid) {
  IntPtr found=IntPtr.Zero;
  EnumChildWindows(dialog,delegate(IntPtr window,IntPtr data){
   uint owner;GetWindowThreadProcessId(window,out owner);
   StringBuilder name=new StringBuilder(128);GetClassName(window,name,128);
   if(owner==pid&&GetDlgCtrlID(window)==id&&name.ToString()=="Button")found=window;
   return true;
  },IntPtr.Zero);
  return found;
 }
 public static string Controls(IntPtr dialog) {
  List<string> result=new List<string>();
  EnumChildWindows(dialog,delegate(IntPtr window,IntPtr data){StringBuilder name=new StringBuilder(128);GetClassName(window,name,128);result.Add(GetDlgCtrlID(window)+":"+name.ToString());return true;},IntPtr.Zero);
  return String.Join(",",result);
 }
}
"@
$root=[System.Windows.Automation.AutomationElement]::RootElement
$condition=[System.Windows.Automation.AndCondition]::new([System.Windows.Automation.Condition[]]@(
 (New-Object System.Windows.Automation.PropertyCondition ([System.Windows.Automation.AutomationElement]::ProcessIdProperty),$OwnedProcessId),
 (New-Object System.Windows.Automation.PropertyCondition ([System.Windows.Automation.AutomationElement]::ClassNameProperty),'#32770'),
 (New-Object System.Windows.Automation.PropertyCondition ([System.Windows.Automation.AutomationElement]::NameProperty),$ExpectedTitle)
))
$deadline=[DateTime]::UtcNow.AddSeconds(15)
$dialog=$null
while(!$dialog -and [DateTime]::UtcNow -lt $deadline){
 $dialog=$root.FindFirst([System.Windows.Automation.TreeScope]::Children,$condition)
 if(!$dialog){Start-Sleep -Milliseconds 50}
}
if(!$dialog){throw 'Owned native file dialog did not appear'}
if($dialog.Current.ProcessId -ne $OwnedProcessId){throw 'Native dialog ownership changed'}
$handle=$dialog.Current.NativeWindowHandle
if($Cancel){
 $buttonId='2'
}else{
 if(!(Test-Path -LiteralPath $FixturePath -PathType Leaf)){throw 'File picker fixture missing'}
 $deadline=[DateTime]::UtcNow.AddSeconds(8)
 $value=$null
 $controls=@()
 while(!$value -and [DateTime]::UtcNow -lt $deadline){
  $edits=$dialog.FindAll([System.Windows.Automation.TreeScope]::Descendants,(New-Object System.Windows.Automation.PropertyCondition ([System.Windows.Automation.AutomationElement]::ControlTypeProperty),([System.Windows.Automation.ControlType]::Edit)))
  $controls=@($edits | ForEach-Object {@{id=$_.Current.AutomationId;name=$_.Current.Name;enabled=$_.Current.IsEnabled;offscreen=$_.Current.IsOffscreen}})
  foreach($edit in $edits){
   if($edit.Current.AutomationId -notin @('1148','1001') -and $edit.Current.Name -notmatch 'File name|\u6587\u4ef6\u540d'){continue}
   if(!$edit.Current.IsEnabled -or $edit.Current.IsOffscreen){continue}
   $candidate=$null
   if($edit.TryGetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern,[ref]$candidate)){$value=$candidate;break}
  }
  if(!$value){Start-Sleep -Milliseconds 50}
 }
 if($value){
  $value.SetValue($FixturePath)
 }else{
  # Some common-dialog edit controls are missing from the UIA control view.
  # Target the documented filename control inside this owned native dialog.
  $edit=[WaterOwnedDialog]::FileName([IntPtr]$handle,$OwnedProcessId)
  if($edit -eq [IntPtr]::Zero){throw ('Native filename control unavailable: '+[WaterOwnedDialog]::Controls([IntPtr]$handle))}
  $result=[IntPtr]::Zero
  if([WaterOwnedDialog]::SendMessageTimeout($edit,0x000c,[IntPtr]::Zero,$FixturePath,2,2000,[ref]$result) -eq [IntPtr]::Zero){throw 'Native filename control did not accept fixture path'}
 }
 $buttonId='1'
}
$buttonCondition=[System.Windows.Automation.AndCondition]::new(
 (New-Object System.Windows.Automation.PropertyCondition ([System.Windows.Automation.AutomationElement]::AutomationIdProperty),$buttonId),
 (New-Object System.Windows.Automation.PropertyCondition ([System.Windows.Automation.AutomationElement]::ControlTypeProperty),([System.Windows.Automation.ControlType]::Button)))
$button=$dialog.FindFirst([System.Windows.Automation.TreeScope]::Descendants,$buttonCondition)
$invoke=$null
if($button -and $button.TryGetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern,[ref]$invoke)){
 $invoke.Invoke()
}else{
 $nativeButton=[WaterOwnedDialog]::Button([IntPtr]$handle,[int]$buttonId,$OwnedProcessId)
 if($nativeButton -eq [IntPtr]::Zero){throw ('Owned native action unavailable: '+[WaterOwnedDialog]::Controls([IntPtr]$handle))}
 if(![WaterOwnedDialog]::PostMessage([IntPtr]$handle,0x111,[IntPtr]([int]$buttonId),$nativeButton)){throw 'Native dialog command failed'}
}
$deadline=[DateTime]::UtcNow.AddSeconds(10)
while([DateTime]::UtcNow -lt $deadline){
 if(!$root.FindFirst([System.Windows.Automation.TreeScope]::Children,$condition)){break}
 Start-Sleep -Milliseconds 50
}
if($root.FindFirst([System.Windows.Automation.TreeScope]::Children,$condition)){throw 'Native dialog did not close'}
@{process_id=$OwnedProcessId;dialog_handle=$handle;title=$ExpectedTitle;action=$(if($Cancel){'cancel'}else{'select'});fixture=$(if($Cancel){$null}else{$FixturePath});closed=$true}|ConvertTo-Json
