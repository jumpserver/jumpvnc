// SPDX-License-Identifier: GPL-2.0-or-later
#include "console_login.h"
#include <sddl.h>
#include <wincred.h>
#include <wtsapi32.h>
#include <cwchar>
#include <cstring>
#include <memory>

namespace console_login {

HRESULT selectUserTile(ICredentialProviderUserArray* users, PSID requested, UserTile& tile) {
    tile = UserTile::Hidden;
    if (!users || !requested || !IsValidSid(requested)) return E_INVALIDARG;
    DWORD count = 0;
    HRESULT hr = users->GetCount(&count);
    if (hr != S_OK) return FAILED(hr) ? hr : E_UNEXPECTED;
    CREDENTIAL_PROVIDER_ACCOUNT_OPTIONS options = CPAO_NONE;
    hr = users->GetAccountOptions(&options);
    if (hr != S_OK) return FAILED(hr) ? hr : E_UNEXPECTED;
    for (DWORD i = 0; i < count; ++i) {
        ICredentialProviderUser* user = nullptr;
        hr = users->GetAt(i, &user);
        if (hr != S_OK || !user) return FAILED(hr) ? hr : E_UNEXPECTED;
        LPWSTR text = nullptr;
        hr = user->GetSid(&text);
        user->Release();
        PSID sid = nullptr;
        bool valid = hr == S_OK && text && ConvertStringSidToSidW(text, &sid);
        CoTaskMemFree(text);
        if (!valid) return FAILED(hr) ? hr : E_UNEXPECTED;
        bool matches = EqualSid(sid, requested);
        LocalFree(sid);
        if (matches) {
            tile = UserTile::EnumeratedUser;
            return S_OK;
        }
    }
    // An empty connected-account tile is for Microsoft accounts. JumpVNC
    // accepts only local SAM accounts, even when Windows shows "Other user".
    if (options & CPAO_EMPTY_LOCAL) {
        tile = UserTile::OtherLocal;
        return S_OK;
    }
    return S_FALSE;
}

HeaderState readHeader(const BYTE* bytes, DWORD available, DWORD session, WORD& units) {
    if (available < headerBytes) return HeaderState::Waiting;
    DWORD sourceSession;
    memcpy(&sourceSession, bytes + 4, sizeof(sourceSession));
    memcpy(&units, bytes + 8, sizeof(units));
    if (memcmp(bytes, "JVC1", 4) || sourceSession != session || !units || units > maxUnits)
        return HeaderState::Invalid;
    return available < headerBytes + units * sizeof(wchar_t) ? HeaderState::Waiting : HeaderState::Ready;
}

bool isPhysicalConsole(DWORD session) {
    DWORD own = 0;
    return session != 0 && session != 0xffffffff && ProcessIdToSessionId(GetCurrentProcessId(), &own) &&
           session == own && WTSGetActiveConsoleSessionId() == session;
}

HANDLE openServicePipe() {
    HANDLE pipe = CreateFileW(LR"(\\.\pipe\JumpVNC-ConsoleLogon-v1)", GENERIC_READ | GENERIC_WRITE, 0, nullptr,
                              OPEN_EXISTING, FILE_FLAG_OVERLAPPED | SECURITY_SQOS_PRESENT | SECURITY_ANONYMOUS, nullptr);
    if (pipe == INVALID_HANDLE_VALUE) return pipe;
    ULONG owner = 0;
    SERVICE_STATUS_PROCESS status{};
    DWORD size = 0;
    SC_HANDLE scm = OpenSCManagerW(nullptr, nullptr, SC_MANAGER_CONNECT);
    SC_HANDLE service = scm ? OpenServiceW(scm, L"SuperVNC", SERVICE_QUERY_STATUS) : nullptr;
    bool trusted = service && GetNamedPipeServerProcessId(pipe, &owner) &&
                   QueryServiceStatusEx(service, SC_STATUS_PROCESS_INFO,
                                        reinterpret_cast<BYTE*>(&status), sizeof(status), &size) &&
                   status.dwCurrentState == SERVICE_RUNNING && owner != 0 && owner == status.dwProcessId;
    if (service) CloseServiceHandle(service);
    if (scm) CloseServiceHandle(scm);
    if (trusted) return pipe;
    CloseHandle(pipe);
    return INVALID_HANDLE_VALUE;
}

bool resolveLocalAccount(const wchar_t* input, WORD units, Account& account) {
    if (!input || !units || units > maxUnits || wmemchr(input, 0, units)) return false;
    if (units >= 2 && input[0] == L'.' && input[1] == L'\\') {
        input += 2;
        units -= 2;
    }
    if (!units || wmemchr(input, L'\\', units) || wmemchr(input, L'/', units) || wmemchr(input, L'@', units))
        return false;
    wmemcpy(account.user, input, units);
    account.user[units] = 0;
    DWORD computerLength = ARRAYSIZE(account.computer);
    if (!GetComputerNameW(account.computer, &computerLength)) return false;
    wchar_t qualified[MAX_COMPUTERNAME_LENGTH + maxUnits + 2]{};
    swprintf(qualified, ARRAYSIZE(qualified), L"%ls\\%ls", account.computer, account.user);
    wchar_t domain[MAX_COMPUTERNAME_LENGTH + 1]{};
    DWORD sidSize = sizeof(account.sid), domainLength = ARRAYSIZE(domain);
    SID_NAME_USE type;
    return LookupAccountNameW(nullptr, qualified, account.sid, &sidSize, domain, &domainLength, &type) &&
           type == SidTypeUser && _wcsicmp(domain, account.computer) == 0;
}

bool canSignIn(DWORD session, CREDENTIAL_PROVIDER_USAGE_SCENARIO scenario, const Account& account) {
    if (!isPhysicalConsole(session) || (scenario != CPUS_LOGON && scenario != CPUS_UNLOCK_WORKSTATION)) return false;
    wchar_t* current = nullptr;
    DWORD bytes = 0;
    if (!WTSQuerySessionInformationW(WTS_CURRENT_SERVER_HANDLE, session, WTSUserName, &current, &bytes))
        return false;
    bool vacant = current && bytes >= sizeof(wchar_t) && !current[0];
    bool same = current && _wcsicmp(current, account.user) == 0;
    WTSFreeMemory(current);
    if (vacant) return scenario == CPUS_LOGON;
    if (!same) return false;
    wchar_t* domain = nullptr;
    if (!WTSQuerySessionInformationW(WTS_CURRENT_SERVER_HANDLE, session, WTSDomainName, &domain, &bytes))
        return false;
    same = domain && _wcsicmp(domain, account.computer) == 0;
    WTSFreeMemory(domain);
    if (!same) return false;
    Account currentAccount{};
    return resolveLocalAccount(account.user, static_cast<WORD>(wcslen(account.user)), currentAccount) &&
           EqualSid(currentAccount.sid, const_cast<BYTE*>(account.sid));
}

bool pipeTransfer(HANDLE pipe, void* buffer, DWORD size, bool write) {
    BYTE* data = static_cast<BYTE*>(buffer);
    const ULONGLONG deadline = GetTickCount64() + 750;
    OVERLAPPED op{};
    op.hEvent = CreateEventW(nullptr, TRUE, FALSE, nullptr);
    if (!op.hEvent) return false;
    while (size) {
        ResetEvent(op.hEvent);
        DWORD count = 0;
        BOOL ok = write ? WriteFile(pipe, data, size, &count, &op) : ReadFile(pipe, data, size, &count, &op);
        if (!ok && GetLastError() == ERROR_IO_PENDING) {
            ULONGLONG now = GetTickCount64();
            DWORD wait = now < deadline ? static_cast<DWORD>(deadline - now) : 0;
            if (WaitForSingleObject(op.hEvent, wait) != WAIT_OBJECT_0) {
                CancelIoEx(pipe, &op);
                GetOverlappedResult(pipe, &op, &count, TRUE);
                break;
            }
            ok = GetOverlappedResult(pipe, &op, &count, FALSE);
        }
        if (!ok || !count) break;
        data += count;
        size -= count;
    }
    CloseHandle(op.hEvent);
    return size == 0;
}

static HRESULT negotiatePackage(ULONG* package) {
    LSA_HANDLE handle;
    NTSTATUS status = LsaConnectUntrusted(&handle);
    if (status != 0) return HRESULT_FROM_WIN32(LsaNtStatusToWinError(status));
    char name[] = "Negotiate";
    LSA_STRING value{sizeof(name) - 1, sizeof(name), name};
    status = LsaLookupAuthenticationPackage(handle, &value, package);
    LsaDeregisterLogonProcess(handle);
    return HRESULT_FROM_WIN32(LsaNtStatusToWinError(status));
}

struct Password {
    wchar_t value[maxUnits + 1]{};
    ~Password() { SecureZeroMemory(value, sizeof(value)); }
};

HRESULT serialize(HANDLE pipe, const Account& account, CREDENTIAL_PROVIDER_USAGE_SCENARIO scenario,
                  CREDENTIAL_PROVIDER_CREDENTIAL_SERIALIZATION* serialized) {
    ULONG package;
    HRESULT hr = negotiatePackage(&package);
    if (FAILED(hr)) return hr;
    char request = 'G';
    WORD units = 0;
    Password password;
    if (!pipeTransfer(pipe, &request, 1, true) || !pipeTransfer(pipe, &units, sizeof(units), false) ||
        units == 0 || units > maxUnits || !pipeTransfer(pipe, password.value, units * sizeof(wchar_t), false) ||
        wmemchr(password.value, 0, units)) return E_ACCESSDENIED;
    DWORD needed = 0;
    if (CredProtectW(FALSE, password.value, units + 1, nullptr, &needed, nullptr) ||
        GetLastError() != ERROR_INSUFFICIENT_BUFFER || !needed || needed > 0x7fff) return E_FAIL;
    std::unique_ptr<wchar_t[]> protectedValue(new (std::nothrow) wchar_t[needed]{});
    if (!protectedValue) return E_OUTOFMEMORY;
    DWORD capacity = needed;
    BOOL ok = CredProtectW(FALSE, password.value, units + 1, protectedValue.get(), &capacity, nullptr);
    SecureZeroMemory(password.value, sizeof(password.value));
    if (!ok) {
        SecureZeroMemory(protectedValue.get(), needed * sizeof(wchar_t));
        return E_FAIL;
    }
    size_t total = sizeof(KERB_INTERACTIVE_UNLOCK_LOGON) +
                   sizeof(wchar_t) * (wcslen(account.computer) + wcslen(account.user) + wcslen(protectedValue.get()));
    auto* packed = static_cast<KERB_INTERACTIVE_UNLOCK_LOGON*>(CoTaskMemAlloc(total));
    if (!packed) {
        SecureZeroMemory(protectedValue.get(), needed * sizeof(wchar_t));
        return E_OUTOFMEMORY;
    }
    ZeroMemory(packed, sizeof(*packed));
    packed->Logon.MessageType = scenario == CPUS_LOGON ? KerbInteractiveLogon : KerbWorkstationUnlockLogon;
    // Negotiate expects UNICODE_STRING buffers as offsets into this allocation.
    size_t offset = sizeof(*packed);
    auto append = [&](UNICODE_STRING& field, const wchar_t* value) {
        field.Length = field.MaximumLength = static_cast<USHORT>(wcslen(value) * sizeof(wchar_t));
        field.Buffer = reinterpret_cast<PWSTR>(offset);
        memcpy(reinterpret_cast<BYTE*>(packed) + offset, value, field.Length);
        offset += field.Length;
    };
    append(packed->Logon.LogonDomainName, account.computer);
    append(packed->Logon.UserName, account.user);
    append(packed->Logon.Password, protectedValue.get());
    SecureZeroMemory(protectedValue.get(), needed * sizeof(wchar_t));
    char submitted = 'S';
    if (!pipeTransfer(pipe, &submitted, 1, true)) {
        SecureZeroMemory(packed, total);
        CoTaskMemFree(packed);
        return E_ABORT;
    }
    serialized->ulAuthenticationPackage = package;
    serialized->clsidCredentialProvider = CLSID_JumpVNCLogin;
    serialized->cbSerialization = static_cast<ULONG>(total);
    serialized->rgbSerialization = reinterpret_cast<BYTE*>(packed);
    return S_OK;
}
}
