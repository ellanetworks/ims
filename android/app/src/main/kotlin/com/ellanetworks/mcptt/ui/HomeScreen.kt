package com.ellanetworks.mcptt.ui

import android.os.SystemClock
import androidx.compose.foundation.Image
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.WindowInsetsSides
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.only
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeGestures
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.Block
import androidx.compose.material.icons.rounded.BluetoothAudio
import androidx.compose.material.icons.rounded.CallEnd
import androidx.compose.material.icons.rounded.ExpandMore
import androidx.compose.material.icons.rounded.Groups
import androidx.compose.material.icons.rounded.Headset
import androidx.compose.material.icons.rounded.Mic
import androidx.compose.material.icons.rounded.PauseCircle
import androidx.compose.material.icons.rounded.PhoneInTalk
import androidx.compose.material.icons.rounded.Settings
import androidx.compose.material.icons.rounded.VolumeUp
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.core.telecom.CallEndpointCompat
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.ellanetworks.mcptt.R
import com.ellanetworks.mcptt.Status
import com.ellanetworks.mcptt.call.CallInfo
import com.ellanetworks.mcptt.call.groupName
import com.ellanetworks.mcptt.service.text
import com.ellanetworks.mcptt.session.Event
import com.ellanetworks.mcptt.settings.UserSettings
import com.ellanetworks.mcptt.ui.theme.EllaTheme
import com.ellanetworks.mcptt.ui.theme.Spacing
import kotlinx.coroutines.delay

/** How long a transient floor state (busy, revoked) stays shown, then the call's state again. */
internal const val TRANSIENT_MS = 2_000L

/** The name a user is shown by: their alias, until the CMS, the user part of their MCPTT ID. */
fun alias(mcpttId: String): String = groupName(mcpttId)

/**
 * The one operational screen, as a radio's: the selected group and its call, the floor's state, the PTT; the
 * registration and what the app needs as banners; settings, diagnostics among them, behind the top bar's button.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun HomeScreen(vm: AppViewModel, onSettings: () -> Unit, onDiagnostics: () -> Unit, modifier: Modifier = Modifier) {
    val status by vm.status.collectAsStateWithLifecycle()
    val calls by vm.calls.collectAsStateWithLifecycle()
    val settings by vm.settings.collectAsStateWithLifecycle()
    val microphone by vm.microphone.collectAsStateWithLifecycle()
    val needs = rememberNeeds(vm)
    var lost by remember { mutableStateOf<String?>(null) }
    var failed by remember { mutableStateOf<Failed?>(null) }
    var picking by remember { mutableStateOf(false) }

    // A call ends of itself, its hang time over (TS 22.179 R-6.2.4-003): unremarked, the group's PTT the same. Only
    // its loss is told, and a press that set up no call, why on the PTT (R-6.2.3.2-002).
    LaunchedEffect(Unit) {
        vm.ended.collect { e ->
            if (e.cause == Event.Cause.ERROR && e.call.state == CallInfo.State.ACTIVE) lost = e.call.group
            if (e.setupFailed) failed = Failed(e.call.group, e.status, SystemClock.elapsedRealtime())
        }
    }

    val groups = settings?.groups.orEmpty()
    val group = settings?.group
    val call = calls.lastOrNull { it.group == group }
    val ringing = calls.filter { it.state == CallInfo.State.RINGING }
    val others = calls.filter { it.group != group && it.state != CallInfo.State.RINGING }

    Scaffold(
        modifier = modifier,
        topBar = { TopBar(status, onSettings) },
    ) { padding ->
        Column(
            Modifier.fillMaxSize().padding(padding).padding(horizontal = Spacing.gutter),
            verticalArrangement = Arrangement.spacedBy(Spacing.m),
        ) {
            val blocking = needs.firstOrNull { it.blocking }
            if (blocking != null) {
                EmptyState(stringResource(blocking.text), null, Modifier.weight(1f))
                return@Column
            }
            Column(
                Modifier.weight(1f).verticalScroll(rememberScrollState()),
                verticalArrangement = Arrangement.spacedBy(Spacing.m),
            ) {
                Spacer(Modifier)
                RegistrationBanner(status, vm, onSettings, onDiagnostics)
                needs.firstOrNull()?.let { n ->
                    Banner(
                        stringResource(n.text),
                        EllaTheme.colors.warningContainer,
                        EllaTheme.colors.onWarningContainer,
                        stringResource(R.string.allow),
                        n.action,
                    )
                }
                lost?.let { g ->
                    Banner(
                        stringResource(R.string.call_lost, groupName(g)) + "\n" + stringResource(R.string.call_lost_detail),
                        MaterialTheme.colorScheme.errorContainer,
                        MaterialTheme.colorScheme.onErrorContainer,
                        stringResource(R.string.dismiss),
                        onAction = { lost = null },
                    )
                }
                if (call != null && !microphone && status is Status.Registered) {
                    Banner(stringResource(R.string.call_no_microphone), EllaTheme.colors.warningContainer, EllaTheme.colors.onWarningContainer, null, null)
                }
                for (r in ringing) Incoming(vm, r)
                if (groups.isEmpty() || group == null) {
                    EmptyState(stringResource(R.string.no_groups), stringResource(R.string.no_groups_detail))
                } else {
                    GroupHeader(group, groups.size > 1) { picking = true }
                }
                for (o in others) OtherCall(o) { vm.select(o.group) }
            }
            if (group != null) {
                if (call != null && call.state != CallInfo.State.RINGING) CallControls(vm, call)
                val registered = status is Status.Registered
                val (pttState, label) = pttOf(registered, call, failed?.takeIf { it.group == group })
                Ptt(
                    state = pttState,
                    label = label,
                    talkUntil = call?.talkUntil,
                    talkFor = call?.talkFor,
                    onPress = { if (registered) vm.talk(group) else null },
                    onRelease = { vm.release() },
                    modifier = Modifier.windowInsetsPadding(WindowInsets.safeGestures.only(WindowInsetsSides.Bottom)).padding(bottom = Spacing.m),
                )
            } else {
                Spacer(Modifier.navigationBarsPadding())
            }
        }
    }

    if (picking && settings != null) {
        ModalBottomSheet(onDismissRequest = { picking = false }) {
            GroupPicker(groups, group, calls) {
                vm.select(it)
                picking = false
            }
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun TopBar(status: Status, onSettings: () -> Unit) {
    TopAppBar(
        navigationIcon = {
            Image(
                painterResource(R.drawable.ic_ella_mark),
                contentDescription = null,
                modifier = Modifier.padding(start = Spacing.m, end = Spacing.s).size(32.dp),
            )
        },
        title = { Text(stringResource(R.string.app_name), style = MaterialTheme.typography.titleLarge) },
        actions = {
            RegistrationChip(status)
            IconButton(onClick = onSettings, modifier = Modifier.size(Spacing.fieldTarget)) {
                Icon(Icons.Rounded.Settings, contentDescription = stringResource(R.string.settings))
            }
        },
    )
}

@Composable
private fun RegistrationChip(status: Status) {
    val colors = EllaTheme.colors
    val (fill, content) = when (status) {
        is Status.Registered -> colors.success to colors.onSuccess
        Status.Registering -> colors.info to colors.onInfo
        is Status.Unconfigured, Status.NoNetwork -> colors.warning to colors.onWarning
        is Status.Failed, Status.Rejected -> MaterialTheme.colorScheme.error to MaterialTheme.colorScheme.onError
        Status.Stopped -> MaterialTheme.colorScheme.surfaceContainerHighest to MaterialTheme.colorScheme.onSurface
    }
    Surface(
        color = fill,
        contentColor = content,
        shape = MaterialTheme.shapes.small,
        modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite },
    ) {
        Text(
            stringResource(status.text()),
            style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.padding(horizontal = Spacing.s, vertical = Spacing.xs),
        )
    }
}

/** The registration's trouble, with what to do about it. */
@Composable
private fun RegistrationBanner(status: Status, vm: AppViewModel, onSettings: () -> Unit, onDiagnostics: () -> Unit) {
    val colors = EllaTheme.colors
    when (status) {
        Status.Stopped -> Banner(
            stringResource(R.string.status_stopped),
            MaterialTheme.colorScheme.surfaceContainerHigh,
            MaterialTheme.colorScheme.onSurface,
            stringResource(R.string.sign_in),
            vm::start,
        )

        // The MCPTT ID the user's, in Settings; what an administrator sets, in Settings' Diagnostics.
        is Status.Unconfigured -> {
            val user = UserSettings.MCPTT_ID in status.missing
            Banner(
                stringResource(R.string.status_missing, status.missing.joinToString()),
                colors.warningContainer,
                colors.onWarningContainer,
                stringResource(if (user) R.string.settings else R.string.diagnostics),
                if (user) onSettings else onDiagnostics,
            )
        }

        Status.NoNetwork -> Banner(stringResource(R.string.status_no_network), colors.warningContainer, colors.onWarningContainer, null, null)

        is Status.Failed -> Banner(
            stringResource(R.string.status_failed) + ": " + status.error,
            MaterialTheme.colorScheme.errorContainer,
            MaterialTheme.colorScheme.onErrorContainer,
            null,
            null,
        )

        Status.Rejected -> Banner(
            stringResource(R.string.status_rejected),
            MaterialTheme.colorScheme.errorContainer,
            MaterialTheme.colorScheme.onErrorContainer,
            null,
            null,
        )

        else -> Unit
    }
}

@Composable
fun Banner(text: String, fill: Color, content: Color, action: String?, onAction: (() -> Unit)?, modifier: Modifier = Modifier) {
    Surface(
        color = fill,
        contentColor = content,
        shape = MaterialTheme.shapes.small,
        modifier = modifier.fillMaxWidth().semantics { liveRegion = LiveRegionMode.Polite },
    ) {
        Row(
            Modifier.padding(start = Spacing.m, end = Spacing.s, top = Spacing.s, bottom = Spacing.s).heightIn(min = 40.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(text, Modifier.weight(1f), style = MaterialTheme.typography.bodyMedium)
            if (action != null && onAction != null) {
                TextButton(onClick = onAction, colors = ButtonDefaults.textButtonColors(contentColor = content)) {
                    Text(action, style = MaterialTheme.typography.labelLarge)
                }
            }
        }
    }
}

@Composable
private fun EmptyState(title: String, detail: String?, modifier: Modifier = Modifier) {
    Column(modifier.fillMaxWidth().padding(vertical = Spacing.l), verticalArrangement = Arrangement.spacedBy(Spacing.s)) {
        Text(title, style = MaterialTheme.typography.titleMedium, color = MaterialTheme.colorScheme.onSurface)
        detail?.let { Text(it, style = MaterialTheme.typography.bodyLarge, color = MaterialTheme.colorScheme.onSurfaceVariant) }
    }
}

/** The selected group: tapped, the picker of the others. */
@Composable
private fun GroupHeader(group: String, pickable: Boolean, onPick: () -> Unit) {
    val name = groupName(group)
    val description = stringResource(R.string.group_selected, name)
    Row(
        Modifier
            .fillMaxWidth()
            .heightIn(min = Spacing.fieldTarget)
            .then(if (pickable) Modifier.clickable(role = Role.Button, onClick = onPick) else Modifier)
            .semantics {
                heading()
                contentDescription = description
            },
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(Icons.Rounded.Groups, contentDescription = null, tint = MaterialTheme.colorScheme.primary)
        Spacer(Modifier.size(Spacing.s))
        Text(name, Modifier.weight(1f), style = MaterialTheme.typography.headlineMedium, maxLines = 2, overflow = TextOverflow.Ellipsis)
        if (pickable) Icon(Icons.Rounded.ExpandMore, contentDescription = null)
    }
}

/**
 * The PTT's state and word of the selected group's call: talking, someone else talking, busy for [TRANSIENT_MS] after a
 * request refused, connecting, held; without a call, idle.
 */
@Composable
internal fun pttOf(registered: Boolean, call: CallInfo?, failed: Failed?): Pair<PttState, String> {
    val busy = transient(call?.refused)
    val failing = transient(failed?.at)
    // The user's own grant is no one else talking: before its first frame, or a tap already released.
    val talker = call?.talker?.takeIf { !call.transmitting && call.floor != Event.Notice.GRANTED }?.let(::alias)
    return when {
        !registered -> PttState.UNAVAILABLE to stringResource(R.string.ptt_unavailable)
        call == null && failing && failed != null -> PttState.BUSY to failed.reason()
        call == null -> PttState.IDLE to stringResource(R.string.ptt)
        call.state == CallInfo.State.HELD -> PttState.UNAVAILABLE to stringResource(R.string.call_held)
        call.state == CallInfo.State.CALLING -> PttState.WAITING to stringResource(R.string.call_connecting)
        call.transmitting -> PttState.TALKING to stringResource(R.string.ptt_talking)
        busy && call.floor == Event.Notice.REVOKED -> PttState.BUSY to stringResource(R.string.call_revoked)
        busy -> PttState.BUSY to (talker?.let { stringResource(R.string.call_busy, it) } ?: stringResource(R.string.call_busy_nobody))
        talker != null -> PttState.RECEIVING to stringResource(R.string.call_talker, talker)
        else -> PttState.IDLE to stringResource(R.string.ptt)
    }
}

/** A press of [group] that set up no call (Ended.setupFailed), its INVITE's [status], [at] when. */
internal data class Failed(val group: String, val status: Int, val at: Long)

/** Why the call was not set up, for the user: who else is there, or whether they may. */
@Composable
private fun Failed.reason(): String = when (status) {
    // TS 24.379 §10.1.1.4.1.1: no other member affiliated.
    480 -> stringResource(R.string.call_failed_nobody, groupName(group))

    403 -> stringResource(R.string.call_failed_forbidden)

    else -> stringResource(R.string.call_failed)
}

/**
 * Whether a transient state of [at] (SystemClock.elapsedRealtime) is still shown, for [TRANSIENT_MS]: of its time since,
 * not since this composition, the screen back showing none anew.
 */
@Composable
private fun transient(at: Long?): Boolean {
    val since = at?.let { SystemClock.elapsedRealtime() - it }
    var shown by remember(at) { mutableStateOf(since != null && since < TRANSIENT_MS) }
    LaunchedEffect(at) {
        if (shown && since != null) {
            delay(TRANSIENT_MS - since)
            shown = false
        }
    }
    return shown
}

/** An invitation to answer (manual commencement, TS 24.379 §6.2.3.2): large buttons, for gloves. */
@Composable
private fun Incoming(vm: AppViewModel, call: CallInfo) {
    Surface(
        color = MaterialTheme.colorScheme.primaryContainer,
        contentColor = MaterialTheme.colorScheme.onPrimaryContainer,
        shape = MaterialTheme.shapes.medium,
    ) {
        Column(Modifier.fillMaxWidth().padding(Spacing.m), verticalArrangement = Arrangement.spacedBy(Spacing.m)) {
            Text(groupName(call.group), style = MaterialTheme.typography.headlineMedium)
            call.caller?.let { Text(stringResource(R.string.call_incoming, alias(it)), style = MaterialTheme.typography.headlineSmall) }
            Row(horizontalArrangement = Arrangement.spacedBy(Spacing.m)) {
                OutlinedButton(
                    onClick = { vm.decline(call.id) },
                    modifier = Modifier.weight(1f).heightIn(min = 72.dp),
                    colors = ButtonDefaults.outlinedButtonColors(contentColor = MaterialTheme.colorScheme.error),
                ) { Text(stringResource(R.string.decline), style = MaterialTheme.typography.labelLarge) }
                Button(
                    onClick = { vm.accept(call.id) },
                    modifier = Modifier.weight(1f).heightIn(min = 72.dp),
                    colors = ButtonDefaults.buttonColors(containerColor = EllaTheme.colors.success, contentColor = EllaTheme.colors.onSuccess),
                ) { Text(stringResource(R.string.answer), style = MaterialTheme.typography.labelLarge) }
            }
        }
    }
}

/** The call of another group than the selected one: tapped, that group selected. */
@Composable
private fun OtherCall(call: CallInfo, onSelect: () -> Unit) {
    val line = call.talker?.let { stringResource(R.string.group_in_call_talker, alias(it)) } ?: stringResource(R.string.group_in_call)
    Row(
        Modifier.fillMaxWidth().heightIn(min = Spacing.fieldTarget).clickable(role = Role.Button, onClick = onSelect),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(Icons.Rounded.Groups, contentDescription = null)
        Spacer(Modifier.size(Spacing.s))
        Text(groupName(call.group) + " · " + line, style = MaterialTheme.typography.bodyLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

/** The audio route of the call and the way out of it, apart from the PTT. */
@Composable
private fun CallControls(vm: AppViewModel, call: CallInfo) {
    var routes by remember { mutableStateOf(false) }
    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Spacing.l)) {
        Box(Modifier.weight(1f)) {
            OutlinedButton(
                onClick = {
                    val other = call.routes.filter { it != call.endpoint }
                    when {
                        other.size == 1 -> vm.route(call.id, other.first())
                        other.size > 1 -> routes = true
                    }
                },
                enabled = call.routes.size > 1,
                modifier = Modifier.fillMaxWidth().heightIn(min = Spacing.fieldTarget),
            ) {
                val (icon, label) = route(call.endpoint)
                Icon(icon, contentDescription = null)
                Spacer(Modifier.size(Spacing.s))
                Text(label, style = MaterialTheme.typography.labelLarge)
            }
            DropdownMenu(expanded = routes, onDismissRequest = { routes = false }) {
                for (r in call.routes) {
                    val (icon, label) = route(r)
                    DropdownMenuItem(
                        text = { Text(label) },
                        leadingIcon = { Icon(icon, contentDescription = null) },
                        onClick = {
                            routes = false
                            vm.route(call.id, r)
                        },
                    )
                }
            }
        }
        OutlinedButton(
            onClick = { vm.leave(call.id) },
            colors = ButtonDefaults.outlinedButtonColors(contentColor = MaterialTheme.colorScheme.error),
            modifier = Modifier.heightIn(min = Spacing.fieldTarget),
        ) {
            Icon(Icons.Rounded.CallEnd, contentDescription = null)
            Spacer(Modifier.size(Spacing.s))
            Text(stringResource(R.string.leave), style = MaterialTheme.typography.labelLarge)
        }
    }
}

@Composable
private fun route(type: Int?): Pair<ImageVector, String> = when (type) {
    CallEndpointCompat.TYPE_SPEAKER -> Icons.Rounded.VolumeUp to stringResource(R.string.route_speaker)
    CallEndpointCompat.TYPE_EARPIECE -> Icons.Rounded.PhoneInTalk to stringResource(R.string.route_earpiece)
    CallEndpointCompat.TYPE_BLUETOOTH -> Icons.Rounded.BluetoothAudio to stringResource(R.string.route_bluetooth)
    CallEndpointCompat.TYPE_WIRED_HEADSET -> Icons.Rounded.Headset to stringResource(R.string.route_headset)
    else -> Icons.Rounded.VolumeUp to stringResource(R.string.route_audio)
}

/** The user's groups, the selected one checked: each with its call, if any. */
@Composable
private fun GroupPicker(groups: List<String>, selected: String?, calls: List<CallInfo>, onPick: (String) -> Unit) {
    Column(Modifier.padding(bottom = Spacing.l).navigationBarsPadding()) {
        Text(
            stringResource(R.string.group_choose),
            style = MaterialTheme.typography.titleLarge,
            modifier = Modifier.padding(horizontal = Spacing.gutter, vertical = Spacing.s).semantics { heading() },
        )
        for (g in groups) {
            val call = calls.lastOrNull { it.group == g }
            Row(
                Modifier
                    .fillMaxWidth()
                    .heightIn(min = Spacing.primaryAction)
                    .selectable(selected = g == selected, role = Role.RadioButton) { onPick(g) }
                    .padding(horizontal = Spacing.gutter),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                RadioButton(selected = g == selected, onClick = null)
                Spacer(Modifier.size(Spacing.m))
                Column {
                    Text(groupName(g), style = MaterialTheme.typography.titleMedium)
                    if (call != null) {
                        Text(
                            call.talker?.let { stringResource(R.string.group_in_call_talker, alias(it)) } ?: stringResource(R.string.group_in_call),
                            style = MaterialTheme.typography.bodyMedium,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
                }
            }
        }
    }
}
