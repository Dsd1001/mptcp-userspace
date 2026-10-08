Unicode true
!include "MUI2.nsh"

!ifndef VERSION
!define VERSION "dev"
!endif

!define PRODUCT_NAME "MPTCP Desk"
!define PRODUCT_PUBLISHER "MPTCP Userspace"
!define PRODUCT_EXE "MPTCP-Desk-Windows.exe"
!define ENGINE_EXE "mptcp-engine.exe"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\MPTCP Desk"

Name "${PRODUCT_NAME} ${VERSION}"
OutFile "build\bin\MPTCP-Desk-${VERSION}-Windows-Setup.exe"
InstallDir "$LOCALAPPDATA\Programs\MPTCP Desk"
InstallDirRegKey HKCU "Software\MPTCP Userspace\MPTCP Desk" "InstallDir"
RequestExecutionLevel user
ShowInstDetails nevershow
ShowUninstDetails nevershow
SetCompressor /SOLID lzma

!define MUI_ABORTWARNING
!define MUI_ICON "${NSISDIR}\Contrib\Graphics\Icons\modern-install.ico"
!define MUI_UNICON "${NSISDIR}\Contrib\Graphics\Icons\modern-uninstall.ico"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"
!insertmacro MUI_LANGUAGE "English"

Section "MPTCP Desk" SEC_MAIN
  SetShellVarContext current
  SetOutPath "$INSTDIR"

  ; A normal in-app update asks the old app to stop first. This is only a
  ; defensive fallback for manual installer launches.
  nsExec::ExecToStack 'taskkill /IM "${PRODUCT_EXE}" /F'
  Pop $0
  Pop $1

  File "build\bin\${PRODUCT_EXE}"
  File "build\bin\${ENGINE_EXE}"
  File "build\bin\MPTCP-Desk-Windows.BUILDINFO"
  WriteUninstaller "$INSTDIR\Uninstall.exe"

  WriteRegStr HKCU "Software\MPTCP Userspace\MPTCP Desk" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "${PRODUCT_NAME}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "Publisher" "${PRODUCT_PUBLISHER}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\${PRODUCT_EXE}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoRepair" 1

  CreateDirectory "$SMPROGRAMS\MPTCP Desk"
  CreateShortcut "$SMPROGRAMS\MPTCP Desk\MPTCP Desk.lnk" "$INSTDIR\${PRODUCT_EXE}"
  CreateShortcut "$SMPROGRAMS\MPTCP Desk\Uninstall.lnk" "$INSTDIR\Uninstall.exe"
SectionEnd

Section "Uninstall"
  SetShellVarContext current
  nsExec::ExecToStack 'taskkill /IM "${PRODUCT_EXE}" /F'
  Pop $0
  Pop $1

  Delete "$INSTDIR\${PRODUCT_EXE}"
  Delete "$INSTDIR\${ENGINE_EXE}"
  Delete "$INSTDIR\MPTCP-Desk-Windows.BUILDINFO"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\MPTCP Desk\MPTCP Desk.lnk"
  Delete "$SMPROGRAMS\MPTCP Desk\Uninstall.lnk"
  RMDir "$SMPROGRAMS\MPTCP Desk"

  ; Remove login-start registration but deliberately keep %APPDATA% settings,
  ; DPAPI ciphertext and LKG so reinstall/upgrade does not destroy user config.
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "MPTCP Desk"
  DeleteRegKey HKCU "Software\MPTCP Userspace\MPTCP Desk"
  DeleteRegKey HKCU "${UNINSTALL_KEY}"
SectionEnd
