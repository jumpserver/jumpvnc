// SPDX-License-Identifier: GPL-2.0-or-later
#pragma once
#include <windows.h>
#include <credentialprovider.h>
#include <ntsecapi.h>

// {D55005C6-847D-422B-95D8-C57A72D45D2B}
inline constexpr GUID CLSID_JumpVNCLogin = {
    0xd55005c6, 0x847d, 0x422b, {0x95, 0xd8, 0xc5, 0x7a, 0x72, 0xd4, 0x5d, 0x2b}
};

namespace console_login {
constexpr unsigned maxUnits = 256;
constexpr unsigned headerBytes = 10;
constexpr ULONGLONG requestLifetimeMs = 30000;
enum class HeaderState { Waiting, Invalid, Ready };
enum class UserTile { Hidden, EnumeratedUser, OtherLocal };
HRESULT selectUserTile(ICredentialProviderUserArray* users, PSID requested, UserTile& tile);
// An advised unlock publishes an external invitation through CredentialsChanged.
// Polling inside LogonUI's initial enumeration would consume that transition.
constexpr bool pollDuringEnumeration(CREDENTIAL_PROVIDER_USAGE_SCENARIO scenario, bool advised) {
    return scenario == CPUS_LOGON || (scenario == CPUS_UNLOCK_WORKSTATION && !advised);
}

struct Account {
    wchar_t computer[MAX_COMPUTERNAME_LENGTH + 1]{};
    wchar_t user[maxUnits + 1]{};
    alignas(DWORD) BYTE sid[SECURITY_MAX_SID_SIZE]{};
};

// The header and account are public. The password stays in the service until
// Windows requests serialization on the tile for this specific invitation.
HeaderState readHeader(const BYTE* bytes, DWORD available, DWORD session, WORD& units);
HANDLE openServicePipe();
bool isPhysicalConsole(DWORD session);
bool resolveLocalAccount(const wchar_t* name, WORD units, Account& account);
bool canSignIn(DWORD session, CREDENTIAL_PROVIDER_USAGE_SCENARIO scenario, const Account& account);
bool pipeTransfer(HANDLE pipe, void* buffer, DWORD size, bool write);
HRESULT serialize(HANDLE pipe, const Account& account, CREDENTIAL_PROVIDER_USAGE_SCENARIO scenario,
                  CREDENTIAL_PROVIDER_CREDENTIAL_SERIALIZATION* serialized);
}
