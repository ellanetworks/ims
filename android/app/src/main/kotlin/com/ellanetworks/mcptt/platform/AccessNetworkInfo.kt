package com.ellanetworks.mcptt.platform

/** A cell the device is on, of the radio and duplex its access type names (TS 24.229 §7.2A.4.2). */
sealed interface Cell {
    val mcc: String?
    val mnc: String?
    val tdd: Boolean

    /** An E-UTRAN cell: its TAC of 16 bits on EPC, 24 on 5GC, and its ECI of 28. */
    data class Lte(override val mcc: String?, override val mnc: String?, val tac: Int?, val eci: Int?, override val tdd: Boolean) : Cell

    /** An NR cell: its TAC of 24 bits, and its NCI of 36. */
    data class Nr(override val mcc: String?, override val mnc: String?, val tac: Int?, val nci: Long?, override val tdd: Boolean) : Cell
}

/**
 * The P-Access-Network-Info of [cell] (TS 24.229 §7.2A.4.3): its access type, and its utran-cell-id-3gpp when the
 * cell's identity is known: MCC, MNC, the TAC (4 hexadecimal digits on EPC, 6 on 5GC, item 4; 6 for NR, item 11) and
 * the ECI (7) or NCI (9), in upper-case hexadecimal. The access type alone when it is not (access-info is optional).
 */
fun accessNetworkInfo(cell: Cell, fiveGc: Boolean = false): String {
    val type = when (cell) {
        is Cell.Lte -> if (cell.tdd) "3GPP-E-UTRAN-TDD" else "3GPP-E-UTRAN-FDD"
        is Cell.Nr -> if (cell.tdd) "3GPP-NR-TDD" else "3GPP-NR-FDD"
    }
    val mcc = cell.mcc?.takeIf { it.length == 3 && it.all(Char::isDigit) } ?: return type
    val mnc = cell.mnc?.takeIf { it.length in 2..3 && it.all(Char::isDigit) } ?: return type
    val id = when (cell) {
        is Cell.Lte -> {
            val tac = cell.tac?.takeIf { it in 0..(if (fiveGc) 0xFFFFFF else 0xFFFF) } ?: return type
            val eci = cell.eci?.takeIf { it in 0..0xFFFFFFF } ?: return type
            hex(tac.toLong(), if (fiveGc) 6 else 4) + hex(eci.toLong(), 7)
        }

        is Cell.Nr -> {
            val tac = cell.tac?.takeIf { it in 0..0xFFFFFF } ?: return type
            val nci = cell.nci?.takeIf { it in 0..0xFFFFFFFFFL } ?: return type
            hex(tac.toLong(), 6) + hex(nci, 9)
        }
    }
    return "$type;utran-cell-id-3gpp=$mcc$mnc$id"
}

private fun hex(v: Long, digits: Int) = v.toString(16).uppercase().padStart(digits, '0')

/** Whether E-UTRA band [band] is TDD (TS 36.101 Table 5.5-1: bands 33 to 54). */
fun lteTdd(band: Int): Boolean = band in 33..54

/** Whether NR band [band] is TDD (TS 38.101-1 Table 5.2-1, TS 38.101-2 Table 5.2-1: the TDD bands, and FR2's). */
fun nrTdd(band: Int): Boolean = band in NR_TDD || band >= 257

private val NR_TDD = setOf(34, 38, 39, 40, 41, 46, 47, 48, 50, 51, 53, 54, 77, 78, 79, 90, 96, 101, 102, 104)
