package com.ellanetworks.mcptt.platform

// TS 24.229 clause 7.2A.4.2
sealed interface Cell {
    val mcc: String?
    val mnc: String?
    val tdd: Boolean

    data class Lte(override val mcc: String?, override val mnc: String?, val tac: Int?, val eci: Int?, override val tdd: Boolean) : Cell

    data class Nr(override val mcc: String?, override val mnc: String?, val tac: Int?, val nci: Long?, override val tdd: Boolean) : Cell
}

// TS 24.229 clause 7.2A.4.3
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

// TS 36.101 clause 5.5 table 5.5-1
fun lteTdd(band: Int): Boolean = band in 33..54

// TS 38.101-1 clause 5.2 table 5.2-1
// TS 38.101-2 clause 5.2 table 5.2-1
fun nrTdd(band: Int): Boolean = band in NR_TDD || band >= 257

private val NR_TDD = setOf(34, 38, 39, 40, 41, 46, 47, 48, 50, 51, 53, 54, 77, 78, 79, 90, 96, 101, 102, 104)
